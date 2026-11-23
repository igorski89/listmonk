# Generates internal/store/QUERIES.md (upstream query -> store method) and
# fails if a query in models/queries.go has no mapping. Run from the repo
# root: python3 internal/store/queries_md.py > internal/store/QUERIES.md

import re
src=''.join(l for l in open('models/queries.go') if not l.strip().startswith('//'))
names=re.findall(r'query:"([a-z-]+)"', src)
M = {
'get-dashboard-charts':'Stats.DashboardCharts','get-dashboard-counts':'Stats.DashboardCounts',
'insert-subscriber':'Subscribers.Insert','upsert-subscriber':'Subscribers.Import (subscribe)','upsert-blocklist-subscriber':'Subscribers.Import (blocklist)',
'get-subscriber':'Subscribers.Get','has-subscriber-list':'Subscribers.HasLists','get-subscribers-by-emails':'Subscribers.GetByEmails',
'get-subscriber-lists':'Subscribers.Lists','get-subscriptions':'Subscriptions.List','get-subscriber-lists-lazy':'(inside Subscribers.Get/Query/GetByEmails)',
'update-subscriber':'Subscribers.Update','update-subscriber-with-lists':'Subscribers.UpdateWithLists','blocklist-subscribers':'Subscribers.Blocklist',
'add-subscribers-to-lists':'Subscriptions.Add','delete-subscriptions':'Subscriptions.Remove','delete-unconfirmed-subscriptions':'Subscriptions.DeleteUnconfirmed',
'confirm-subscription-optin':'Subscriptions.ConfirmOptin','unsubscribe-subscribers-from-lists':'Subscriptions.Unsubscribe','delete-subscribers':'Subscribers.Delete',
'delete-blocklisted-subscribers':'Subscribers.DeleteBlocklisted','delete-orphan-subscribers':'Subscribers.DeleteOrphans','unsubscribe-by-campaign':'Subscribers.UnsubscribeByCampaign',
'export-subscriber-data':'Subscribers.Profile','get-subscriber-charts':'Subscribers.Charts','get-subscriber-activity':'Subscribers.Activity',
'delete-subscriber-campaign-views':'Subscribers.DeleteActivity','delete-subscriber-link-clicks':'Subscribers.DeleteActivity',
'query-subscribers':'Subscribers.Query','query-subscribers-count':'(inside Subscribers.Query)','query-subscribers-count-all':'(inside Subscribers.Query, cached)',
'query-subscribers-for-export':'Subscribers.Export','query-subscribers-template':'(inside the …ByFilter methods: Expr → IDs)',
'delete-subscribers-by-query':'Subscribers.DeleteByFilter','add-subscribers-to-lists-by-query':'Subscriptions.AddByFilter','blocklist-subscribers-by-query':'Subscribers.BlocklistByFilter',
'delete-subscriptions-by-query':'Subscriptions.RemoveByFilter','unsubscribe-subscribers-from-lists-by-query':'Subscriptions.UnsubscribeByFilter',
'get-list-analytics-items':'Analytics.Items (lists)','get-campaign-analytics-items':'Analytics.Items (campaigns)','get-list-analytics-counts':'(template for the list Counts below)',
'get-list-view-counts':'Analytics.Counts','get-list-click-counts':'Analytics.Counts','get-list-total-view-counts':'Analytics.Counts','get-list-total-click-counts':'Analytics.Counts',
'get-list-bounce-counts':'Analytics.Counts','get-list-subscription-counts':'Analytics.Counts','get-list-unsubscription-counts':'Analytics.Counts','get-list-link-counts':'Analytics.Links (lists)',
'get-list-charts':'Stats.ListCharts (cached)','get-list-charts-live':'Stats.ListCharts (live)',
'create-list':'Lists.Create','query-lists':'Lists.Query, Lists.Get','get-lists':'Lists.All','get-lists-by-optin':'Lists.ByOptin','get-list-types':'Lists.Types',
'update-list':'Lists.Update','update-lists-date':'Lists.Touch','delete-lists':'Lists.DeleteMany',
'create-campaign':'Campaigns.Create','query-campaigns':'Campaigns.Query','get-campaign':'Campaigns.Get','get-campaign-for-preview':'Campaigns.GetForPreview',
'get-campaign-stats':'(inside Campaigns.Get/Query)','get-campaign-status':'Campaigns.RunningStats','get-archived-campaigns':'Campaigns.Archived','campaign-has-lists':'Campaigns.HasLists',
'get-campaign-activity':'Analytics.Activity','get-campaign-activity-events':'Analytics.ActivityEvents','get-campaign-analytics-counts':'(template for the campaign Counts below)',
'get-campaign-view-counts':'Analytics.Counts','get-campaign-click-counts':'Analytics.Counts','get-campaign-link-counts':'Analytics.Links (campaigns)','get-campaign-bounce-counts':'Analytics.Counts',
'delete-campaign-views':'Tracking.DeleteViewsBefore','delete-campaign-link-clicks':'Tracking.DeleteClicksBefore','export-campaign-views':'Tracking.ExportViews','export-campaign-link-clicks':'Tracking.ExportClicks',
'get-campaign-total-view-counts':'Analytics.Counts','get-campaign-total-click-counts':'Analytics.Counts',
'next-campaigns':'Campaigns.Next','get-running-campaign':'(inside Campaigns.NextSubscribers)','next-campaign-subscribers':'Campaigns.NextSubscribers',
'get-one-campaign-subscriber':'— unused upstream, dropped','update-campaign':'Campaigns.Update','update-campaign-status':'Campaigns.SetStatus','update-campaign-counts':'Campaigns.SetCounts',
'update-campaign-archive':'Campaigns.SetArchive','register-campaign-view':'Tracking.RegisterView','delete-campaign':'Campaigns.Delete','delete-campaigns':'Campaigns.DeleteMany',
'update-media':'Media.SetTags','get-media-tags':'Media.Tags','insert-media':'Media.Insert','get-media':'Media.Get','query-media':'Media.Query','delete-media':'Media.Delete',
'create-template':'Templates.Create','get-templates':'Templates.Query, Templates.Get','update-template':'Templates.Update','set-default-template':'Templates.SetDefault','delete-template':'Templates.Delete',
'create-link':'Tracking.CreateLink','get-link-url':'Tracking.LinkURL','register-link-click':'Tracking.RegisterClick',
'get-settings':'Settings.All','update-settings':'Settings.Update','update-settings-by-key':'Settings.Set',
'record-bounce':'Bounces.Record','query-bounces':'Bounces.Query, Bounces.Get','blocklist-bounced-subscribers':'Bounces.BlocklistSubscribers','delete-bounces':'Bounces.Delete',
'delete-bounces-by-subscriber':'Bounces.DeleteForSubscriber','get-db-info':'Schema.Info',
'create-user':'Users.Create','update-user':'Users.Update','update-user-profile':'Users.UpdateProfile','update-user-email':'Users.UpdateEmail','update-user-login':'Users.RecordLogin','set-user-twofa':'Users.SetTwoFA',
'delete-users':'Users.Delete','get-users':'Users.Query','get-user':'Users.Get','get-api-tokens':'— unused upstream, dropped','login-user':'Users.Login','delete-user-sessions':'Users.DeleteSessions',
'create-role':'Roles.CreateUserRole, Roles.CreateListRole','get-user-roles':'Roles.UserRoles, Roles.UserRole','get-list-roles':'Roles.ListRoles','update-role':'Roles.UpdateUserRole, Roles.UpdateListRole',
'delete-role':'Roles.Delete','upsert-list-permissions':'Roles.CreateListRole, Roles.UpdateListRole','delete-list-permission':'Roles.DeleteListPermission',
}
miss=[n for n in names if n not in M]; extra=[k for k in M if k not in names]
assert not miss and not extra, (miss, extra)
print("# Upstream query -> store method\n")
print(f"Generated from models/queries.go at v7.0.0-beta.2: {len(names)} queries, all mapped (checked by script).\n")
print("Other database access outside models.Queries:\n")
for a,b in [("cmd/install.go: schema.sql, sample data","Schema.Install + the other stores"),("cmd/upgrade.go, internal/migrations","Schema.Version/Pending/Migrate"),("cmd/maintenance.go: VACUUM ANALYZE","Schema.Optimize"),("cmd/init.go initSettings","Settings.All"),("internal/core RefreshMatView(s)","Stats.Refresh"),("internal/auth: simplesessions postgres store","Sessions()"),("internal/subimporter: raw *sql.Stmt + tx","Subscribers.Import, Lists.Touch"),("internal/bounce: own DB + RecordQuery","Bounces.Record")]:
    print(f"- {a} → `{b}`")
print("\n| Query | Store method |\n|---|---|")
for n in names: print(f"| `{n}` | {M[n]} |")
