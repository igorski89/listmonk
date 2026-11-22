package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/knadh/listmonk/internal/auth"
	"github.com/knadh/listmonk/internal/messenger/email"
	lmstore "github.com/knadh/listmonk/internal/store"
	"github.com/knadh/listmonk/internal/store/postgres"
	"github.com/knadh/listmonk/internal/utils"
	"github.com/knadh/listmonk/models"
	"github.com/knadh/stuffbin"
	null "gopkg.in/volatiletech/null.v6"
)

// install runs the first time setup of setting up the database.
func install(lastVer string, st *postgres.Store, fs stuffbin.FileSystem, prompt, idempotent bool) {
	ctx := context.Background()

	fmt.Println("")
	if !idempotent {
		fmt.Println("** first time installation **")
		fmt.Printf("** IMPORTANT: This will wipe existing listmonk tables and types in the DB '%s' **",
			ko.String("db.database"))
	} else {
		fmt.Println("** first time (idempotent) installation **")
	}
	fmt.Println("")

	if prompt {
		var ok string
		fmt.Print("continue (y/N)?  ")
		if _, err := fmt.Scanf("%s", &ok); err != nil {
			lo.Fatalf("error reading value from terminal: %v", err)
		}
		if strings.ToLower(ok) != "y" {
			fmt.Println("install cancelled.")
			return
		}
	}

	// If idempotence is on, check if the DB is already setup.
	if idempotent {
		// A missing schema means a fresh install.
		// Abort on all other errors to not end up in a half-baked state.
		if ok, err := st.Schema().Installed(ctx); err != nil {
			lo.Fatalf("error checking existing DB schema: %v", err)
		} else if ok {
			lo.Println("skipping install as database appears to be already setup")
			os.Exit(0)
		}
	}

	// Migrate the tables.
	if err := st.Schema().Install(ctx, lastVer, lmstore.MigrationEnv{FS: fs, Config: ko, Log: lo}); err != nil {
		lo.Fatalf("error migrating DB schema: %v", err)
	}

	// Load the queries.
	configureStore(st, ko)

	// Sample list.
	defList, optinList := installLists(st)

	// Sample subscribers.
	installSubs(defList, optinList, st)

	// Templates.
	campTplID, archiveTplID := installTemplates(st)

	// Sample campaign.
	installCampaign(campTplID, archiveTplID, st)

	// Setup admin user optionally.
	var (
		user     = os.Getenv("LISTMONK_ADMIN_USER")
		password = os.Getenv("LISTMONK_ADMIN_PASSWORD")
		apiUser  = os.Getenv("LISTMONK_ADMIN_API_USER")

		hasUser = false
	)

	// Admin user.
	if user != "" && password != "" {
		if len(user) < 3 || len(password) < 8 {
			lo.Fatal("LISTMONK_ADMIN_USER should be min 3 chars and LISTMONK_ADMIN_PASSWORD should be min 8 chars")
		}

		lo.Printf("creating superadmin user '%s'", user)
		hasUser = true
	} else {
		lo.Printf("no superadmin user created. Visit webpage to create user.")
	}

	// API User.
	if apiUser != "" {
		if !hasUser {
			lo.Fatal("LISTMONK_ADMIN_API_USER requires LISTMONK_ADMIN_USER and LISTMONK_ADMIN_PASSWORD to be set")
		}

		if len(apiUser) < 3 {
			lo.Fatal("LISTMONK_ADMIN_API_USER should be min 3 chars")
		}

		lo.Printf("creating superadmin API user '%s'", apiUser)
	}

	if hasUser {
		installUser(user, password, apiUser, st)
	}

	lo.Printf("setup complete")
	lo.Printf(`run the program and access the dashboard at %s`, ko.MustString("app.address"))
}

func installLists(st lmstore.Store) (int, int) {
	ctx := context.Background()
	defList, err := st.Lists().Create(ctx, models.List{
		Name:   "Default list",
		Type:   models.ListTypePrivate,
		Optin:  models.ListOptinSingle,
		Status: models.ListStatusActive,
		Tags:   []string{"test"},
	})
	if err != nil {
		lo.Fatalf("error creating list: %v", err)
	}

	optinList, err := st.Lists().Create(ctx, models.List{
		Name:   "Opt-in list",
		Type:   models.ListTypePublic,
		Optin:  models.ListOptinDouble,
		Status: models.ListStatusActive,
		Tags:   []string{"test"},
	})
	if err != nil {
		lo.Fatalf("error creating list: %v", err)
	}

	return defList.ID, optinList.ID
}

func installSubs(defListID, optinListID int, st lmstore.Store) {
	ctx := context.Background()
	opt := lmstore.ImportOptions{
		Mode:               lmstore.ImportSubscribe,
		SubscriptionStatus: models.SubscriptionStatusUnconfirmed,
		OverwriteUserInfo:  true,
		OverwriteSubStatus: true,
		Permitted:          lmstore.ListScope{All: true},
	}

	// Sample subscriber.
	opt.ListIDs = []int{defListID}
	if err := st.Subscribers().Import(ctx, []lmstore.SubscriberInput{{
		Email:   "john@example.com",
		Name:    "John Doe",
		Attribs: models.JSON{"type": "known", "good": true, "city": "Bengaluru"},
	}}, opt); err != nil {
		lo.Fatalf("Error creating subscriber: %v", err)
	}

	opt.ListIDs = []int{optinListID}
	if err := st.Subscribers().Import(ctx, []lmstore.SubscriberInput{{
		Email:   "anon@example.com",
		Name:    "Anon Doe",
		Attribs: models.JSON{"type": "unknown", "good": true, "city": "Bengaluru"},
	}}, opt); err != nil {
		lo.Fatalf("error creating subscriber: %v", err)
	}
}

func installTemplates(st lmstore.Store) (int, int) {
	ctx := context.Background()

	// Default campaign template.
	campTpl, err := fs.Get("/static/email-templates/default.tpl")
	if err != nil {
		lo.Fatalf("error reading default e-mail template: %v", err)
	}

	camp, err := st.Templates().Create(ctx, lmstore.TemplateInput{Name: "Default campaign template", Type: models.TemplateTypeCampaign, Body: string(campTpl.ReadBytes())})
	if err != nil {
		lo.Fatalf("error creating default campaign template: %v", err)
	}
	if err := st.Templates().SetDefault(ctx, camp.ID); err != nil {
		lo.Fatalf("error setting default template: %v", err)
	}

	// Default campaign archive template.
	archiveTpl, err := fs.Get("/static/email-templates/default-archive.tpl")
	if err != nil {
		lo.Fatalf("error reading default archive template: %v", err)
	}

	archive, err := st.Templates().Create(ctx, lmstore.TemplateInput{Name: "Default archive template", Type: models.TemplateTypeCampaign, Body: string(archiveTpl.ReadBytes())})
	if err != nil {
		lo.Fatalf("error creating default campaign template: %v", err)
	}

	// Sample tx template.
	txTpl, err := fs.Get("/static/email-templates/sample-tx.tpl")
	if err != nil {
		lo.Fatalf("error reading default e-mail template: %v", err)
	}

	if _, err := st.Templates().Create(ctx, lmstore.TemplateInput{Name: "Sample transactional template", Type: models.TemplateTypeTx,
		Subject: "Welcome {{ .Subscriber.Name }}", Body: string(txTpl.ReadBytes())}); err != nil {
		lo.Fatalf("error creating sample transactional template: %v", err)
	}

	// Sample visual campaign template.
	visualTpl, err := fs.Get("/static/email-templates/default-visual.tpl")
	if err != nil {
		lo.Fatalf("error reading default visual template: %v", err)
	}
	visualSrc, err := fs.Get("/static/email-templates/default-visual.json")
	if err != nil {
		lo.Fatalf("error reading default visual template json: %v", err)
	}

	if _, err := st.Templates().Create(ctx, lmstore.TemplateInput{Name: "Sample visual template", Type: models.TemplateTypeCampaignVisual,
		Body: string(visualTpl.ReadBytes()), BodySource: null.StringFrom(string(visualSrc.ReadBytes()))}); err != nil {
		lo.Fatalf("error creating default campaign template: %v", err)
	}

	return camp.ID, archive.ID
}

func installCampaign(campTplID, archiveTplID int, st lmstore.Store) {
	// Sample campaign.
	if _, err := st.Campaigns().Create(context.Background(), models.Campaign{
		Type:      models.CampaignTypeRegular,
		Name:      "Test campaign",
		Subject:   "Welcome to listmonk",
		FromEmail: "No Reply <noreply@yoursite.com>",
		Body: `<h3>Hi {{ .Subscriber.FirstName }}!</h3>
		<p>This is a test e-mail campaign. Your second name is {{ .Subscriber.LastName }} and you are from {{ .Subscriber.Attribs.city }}.</p>
		<p>Here is a <a href="https://listmonk.app@TrackLink">tracked link</a>.</p>
		<p>Use the link icon in the editor toolbar or when writing raw HTML or Markdown,
			simply suffix @TrackLink to the end of a URL to turn it into a tracking link. Example:</p>
		<pre>&lt;a href=&quot;https:/&zwnj;/listmonk.app&#064;TrackLink&quot;&gt;&lt;/a&gt;</pre>
		<p>For help, refer to the <a href="https://listmonk.app/docs">documentation</a>.</p>
		`,
		ContentType:       "richtext",
		Headers:           models.Headers{},
		Attribs:           models.JSON{},
		Tags:              []string{"test-campaign"},
		Messenger:         email.MessengerName,
		TemplateID:        null.IntFrom(campTplID),
		ArchiveSlug:       null.StringFrom("welcome-to-listmonk"),
		ArchiveTemplateID: null.IntFrom(archiveTplID),
		ArchiveMeta:       json.RawMessage(`{"name": "Subscriber"}`),
	}, []int{1}, nil); err != nil {
		lo.Fatalf("error creating sample campaign: %v", err)
	}
}

func newConfigFile(path string) error {
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return fmt.Errorf("error creating %s: %v", path, err)
	}

	// Initialize the static file system into which all
	// required static assets (.sql, .js files etc.) are loaded.
	fs := initFS(appDir, "", "")
	b, err := fs.Read("config.toml.sample")
	if err != nil {
		return fmt.Errorf("error reading sample config (is binary stuffed?): %v", err)
	}

	return os.WriteFile(path, b, 0644)
}

func installUser(username, password, apiUsername string, st lmstore.Store) {
	ctx := context.Background()
	consts := initConstConfig(ko)

	// Super Admin role gets all permissions.
	perms := []string{}
	for p := range consts.Permissions {
		perms = append(perms, p)
	}

	// Create the Super Admin role in the DB.
	role, err := st.Roles().CreateUserRole(ctx, auth.Role{Name: null.StringFrom("Super Admin"), Permissions: perms})
	if err != nil {
		lo.Fatalf("error creating super admin role: %v", err)
	}

	// Create the admin user.
	if _, err := st.Users().Create(ctx, auth.User{Username: username, PasswordLogin: true, Password: null.StringFrom(password),
		Email: null.StringFrom(username + "@listmonk"), Name: username, Type: auth.RoleTypeUser, UserRoleID: role.ID, Status: auth.UserStatusEnabled}); err != nil {
		lo.Fatalf("error creating superadmin user: %v", err)
	}

	// Create the admin API user.
	if apiUsername != "" {
		// Generate a random API token.
		tk, err := utils.GenerateRandomString(32)
		if err != nil {
			lo.Fatalf("error generating API token: %v", err)
		}

		var (
			email    = null.String{String: apiUsername + "@api", Valid: true}
			password = null.String{String: auth.HashAPIToken(tk), Valid: true}
		)

		if _, err := st.Users().Create(ctx, auth.User{Username: apiUsername, PasswordLogin: false, Password: password,
			Email: email, Name: apiUsername, Type: auth.UserTypeAPI, UserRoleID: role.ID, Status: auth.UserStatusEnabled}); err != nil {
			lo.Fatalf("error creating superadmin API user: %v", err)
		}

		// Print the token to stdout so that it can be grepped out.
		lo.Println("writing API token LISTMONK_ADMIN_API_TOKEN to stderr")
		fmt.Fprintf(os.Stderr, "export LISTMONK_ADMIN_API_TOKEN=\"%s\"\n", tk)
	}
}
