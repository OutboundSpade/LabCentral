package localdb

import (
	"context"
	"github.com/OutboundSpade/LabCentral/internal/application"
	"strings"
	"testing"
)

func TestExtensibleAccessMigrationPreservesPopulatedLegacyRows(t *testing.T) {
	store := newSQLiteAccessContractStore(t)
	ctx := context.Background()
	setupQuotaTestTenancy(t, store)
	instance, digest := createReadyAccessTestInstance(t, store, "instance.migrate")
	legacy := finalizeAccessTestSession(t, store, beginAccessTestSession(t, store, instance, digest, "access.migrate", "migrate"), digest, accessTestHash("legacy-token"), "migrate")
	// Construct the previously shipped table, retaining real rows in its original
	// column order; then exercise the public migration entrypoint on that database.
	previous := accessSessionMigrationStatements(store.integerType(), store.blobType())
	statements := []string{
		"ALTER TABLE labcentral_access_sessions DROP COLUMN delivery_claimed",
		strings.Replace(previous[0], "CREATE TABLE labcentral_access_sessions (", "CREATE TABLE legacy_access (", 1),
		"INSERT INTO legacy_access SELECT * FROM labcentral_access_sessions",
		"DROP TABLE labcentral_access_sessions",
		"ALTER TABLE legacy_access RENAME TO labcentral_access_sessions",
	}
	statements = append(statements, previous[1:]...)
	statements = append(statements, "DELETE FROM labcentral_schema_migrations WHERE id='0014_extensible_access'")
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	migrated, err := store.GetAccessSession(ctx, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Method != legacy.Method || migrated.State != legacy.State || migrated.ProviderStateDigest != legacy.ProviderStateDigest || migrated.LaunchTokenHash != legacy.LaunchTokenHash || string(migrated.AccessState) != string(legacy.AccessState) || migrated.ResourceVersion != legacy.ResourceVersion {
		t.Fatal("legacy access record changed during migration")
	}
	if _, err := store.ClaimAccessDelivery(ctx, migrated.ID, migrated.SubjectID, []application.KeyedTokenHash{accessTestHash("legacy-token")}, governanceIDs("migrated-claim")); err != nil {
		t.Fatal(err)
	}
}
