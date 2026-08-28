package migrations

import (
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEmbeddedMigrationsAreSequentialForwardOnlyAndTransactional(t *testing.T) {
	entries, err := fs.Glob(FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := []string{
		"000001_identity.up.sql",
		"000002_domain.up.sql",
		"000003_agent_sync_audit.up.sql",
		"000004_security_functions_rls.up.sql",
	}
	if len(entries) < len(wantPrefix) {
		t.Fatalf("migration files = %#v, want at least %#v", entries, wantPrefix)
	}
	for index, want := range wantPrefix {
		if entries[index] != want {
			t.Fatalf("migration %d = %q, want %q", index, entries[index], want)
		}
	}
	for index, name := range entries {
		if filepath.Ext(name) != ".sql" || !strings.HasSuffix(name, ".up.sql") {
			t.Fatalf("migration %q is not a forward .up.sql file", name)
		}
		version, parseErr := strconv.Atoi(name[:6])
		if parseErr != nil || version != index+1 {
			t.Fatalf("migration %q is not sequential at position %d", name, index+1)
		}
		contents, readErr := fs.ReadFile(FS, name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		sql := strings.TrimSpace(string(contents))
		upper := strings.ToUpper(sql)
		if !strings.HasPrefix(upper, "BEGIN;") || !strings.HasSuffix(upper, "COMMIT;") {
			t.Errorf("%s must be wrapped in an explicit transaction", name)
		}
		for _, placeholder := range []string{"TODO", "TBD", "FIXME"} {
			if strings.Contains(upper, "-- "+placeholder) || strings.Contains(upper, "/* "+placeholder) {
				t.Errorf("%s contains placeholder %s", name, placeholder)
			}
		}
	}
}

func TestEmbeddedMigrationsContainApprovedSchemaAndSecurityBoundaries(t *testing.T) {
	var schema strings.Builder
	entries, err := fs.Glob(FS, "*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		contents, readErr := fs.ReadFile(FS, name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		schema.Write(contents)
		schema.WriteByte('\n')
	}
	sql := strings.ToLower(schema.String())

	wantTables := []string{
		"users", "sessions", "account_tokens", "login_throttles", "user_settings", "user_devices",
		"goals", "goal_milestones", "tasks", "calendar_events", "calendar_event_reminders",
		"records", "notes", "daily_reviews", "tags", "record_tags", "note_tags", "entity_links",
		"agent_runs", "agent_steps", "agent_changes", "agent_source_refs", "audit_events",
		"audit_event_entities", "sync_changes", "client_mutations", "outbox_events",
	}
	for _, table := range wantTables {
		if !strings.Contains(sql, "create table dayorder."+table) {
			t.Errorf("schema is missing table dayorder.%s", table)
		}
	}

	wantSecurity := []string{
		"enable row level security",
		"create policy tenant_isolation",
		"dayorder.current_user_id()",
		"security definer",
		"for update skip locked",
		"dayorder.outbox_metrics()",
		"to dayorder_api",
		"to dayorder_worker",
		"foreign key (user_id, goal_id)",
		"foreign key (user_id, device_id)",
	}
	for _, fragment := range wantSecurity {
		if !strings.Contains(sql, fragment) {
			t.Errorf("schema is missing security fragment %q", fragment)
		}
	}
	if strings.Contains(sql, "unique (id, id)") {
		t.Error("schema contains a meaningless duplicate-column unique constraint")
	}
}
