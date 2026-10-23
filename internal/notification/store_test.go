package notification_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/notification"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// The tests run as the application role, never the owner: a superuser bypasses
// row-level security even with FORCE, which would make the isolation
// assertions vacuously true.

type fixture struct {
	t   *testing.T
	app *sql.DB
	own *pgtest.DB
	st  *notification.Store
	t1  tenancy.Scope
	t2  tenancy.Scope
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	app.SetMaxOpenConns(6)

	for _, id := range []string{"T1", "T2"} {
		if _, err := app.Exec(`SELECT auth_register_tenant($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, "Tenant "+id, strings.ToLower(id), "U-"+id, strings.ToLower(id)+"@example.com",
			"x", "User "+id, "M-"+id); err != nil {
			t.Fatalf("seed tenant %s: %v", id, err)
		}
	}

	// A second real user of the tenant, written as the owner: the app role
	// may only read the identity table.
	if _, err := db.Exec(`INSERT INTO users (id, email, password_hash, name)
		VALUES ('U-second', 'second@example.com', 'x', 'Second')`); err != nil {
		t.Fatalf("seed second user: %v", err)
	}

	return &fixture{
		t:   t,
		app: app,
		own: db,
		st:  notification.NewStore(app),
		t1:  tenancy.Scope{TenantID: "T1", UserID: "U-T1", Role: tenancy.RoleOwner},
		t2:  tenancy.Scope{TenantID: "T2", UserID: "U-T2", Role: tenancy.RoleOwner},
	}
}

func TestCreateAndListNotifications(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.st.Create(ctx, f.t1, notification.New{
		UserID:  "U-T1",
		Kind:    notification.KindCheckpointPending,
		TitleMD: "Checkpoint waiting",
		BodyMD:  "approve the spec",
		Link:    "/runs/R1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.TenantID != "T1" || created.UserID != "U-T1" {
		t.Fatalf("unexpected entry: %+v", created)
	}
	if created.ReadAt != nil {
		t.Fatal("a new notification must be unread")
	}

	list, err := f.st.List(ctx, f.t1, "U-T1", false, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}

	// Another user of the same tenant sees nothing.
	list, err = f.st.List(ctx, f.t1, "U-other", false, 10)
	if err != nil {
		t.Fatalf("list other user: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("another user sees %d notifications", len(list))
	}
}

func TestUnreadCountAndMarkRead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := f.st.Create(ctx, f.t1, notification.New{
			UserID: "U-T1", Kind: notification.KindRunFailed, TitleMD: "run failed",
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	count, err := f.st.CountUnread(ctx, f.t1, "U-T1")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Fatalf("unread = %d, want 3", count)
	}

	list, err := f.st.List(ctx, f.t1, "U-T1", true, 10)
	if err != nil {
		t.Fatalf("list unread: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("unread list = %d", len(list))
	}

	if _, err := f.st.MarkRead(ctx, f.t1, list[0].ID, "U-T1"); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	count, err = f.st.CountUnread(ctx, f.t1, "U-T1")
	if err != nil {
		t.Fatalf("count after read: %v", err)
	}
	if count != 2 {
		t.Fatalf("unread after read = %d, want 2", count)
	}

	// Reading twice is reported, not silently accepted.
	if _, err := f.st.MarkRead(ctx, f.t1, list[0].ID, "U-T1"); err != notification.ErrAlreadyRead {
		t.Fatalf("second read = %v, want ErrAlreadyRead", err)
	}
	// Another user cannot read somebody else's notification.
	if _, err := f.st.MarkRead(ctx, f.t1, list[1].ID, "U-other"); err != notification.ErrNotFound {
		t.Fatalf("read by another user = %v, want ErrNotFound", err)
	}
}

func TestNotificationsAreTenantScoped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.st.Create(ctx, f.t1, notification.New{
		UserID: "U-T1", Kind: notification.KindSyncError, TitleMD: "sync failed",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The other tenant cannot see the row, and cannot read it.
	list, err := f.st.List(ctx, f.t2, "U-T1", false, 10)
	if err != nil {
		t.Fatalf("list in other tenant: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("other tenant sees %d notifications", len(list))
	}
	if _, err := f.st.MarkRead(ctx, f.t2, created.ID, "U-T1"); err != notification.ErrNotFound {
		t.Fatalf("cross-tenant read = %v, want ErrNotFound", err)
	}
}

func TestInvalidNotificationInput(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.st.Create(ctx, f.t1, notification.New{Kind: notification.KindRunFailed}); err != notification.ErrInvalidUser {
		t.Fatalf("missing user = %v, want ErrInvalidUser", err)
	}
	if _, err := f.st.Create(ctx, f.t1, notification.New{UserID: "U-T1", Kind: "nope"}); err != notification.ErrInvalidKind {
		t.Fatalf("bad kind = %v, want ErrInvalidKind", err)
	}
}

func TestCreateManyNotifiesEveryRecipient(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	entries, err := f.st.CreateMany(ctx, f.t1, notification.New{
		Kind: notification.KindCheckpointPending, TitleMD: "two checkpoints",
	}, []string{"U-T1", "U-second"})
	if err != nil {
		t.Fatalf("create many: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	for _, user := range []string{"U-T1", "U-second"} {
		list, err := f.st.List(ctx, f.t1, user, false, 10)
		if err != nil {
			t.Fatalf("list %s: %v", user, err)
		}
		if len(list) != 1 {
			t.Fatalf("%s sees %d notifications", user, len(list))
		}
	}
}
