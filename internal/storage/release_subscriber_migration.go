package storage

import (
	"fmt"
	"slices"
	"sort"

	"gorm.io/gorm"
)

var releaseSubscriberPrimaryKeys = map[string][]string{
	"release_subscriber_states": {
		"namespace_id", "release_name", "client_name", "instance_id", "identity", "state",
	},
	"release_subscriber_connections": {
		"namespace_id", "release_name", "client_name", "instance_id", "identity",
	},
}

type sqliteTableColumn struct {
	Name string
	PK   int `gorm:"column:pk"`
}

// releaseSubscriberTablePrimaryKey reads the physical SQLite primary key in
// key order. Schema-version stamps alone are not trusted: a process may have
// been interrupted after a partial/manual migration or the stamp may have been
// restored independently from the schema.
func releaseSubscriberTablePrimaryKey(db *gorm.DB, table string) ([]string, bool, error) {
	if !db.Migrator().HasTable(table) {
		return nil, false, nil
	}
	var columns []sqliteTableColumn
	if err := db.Raw("PRAGMA table_info(`" + table + "`)").Scan(&columns).Error; err != nil {
		return nil, true, fmt.Errorf("inspect %s primary key: %w", table, err)
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].PK < columns[j].PK })
	key := make([]string, 0, len(columns))
	for _, column := range columns {
		if column.PK > 0 {
			key = append(key, column.Name)
		}
	}
	return key, true, nil
}

// ensureReleaseSubscriberIdentityKeys repairs either subscriber table
// independently whenever its physical key is stale. SQLite cannot alter a
// composite primary key in place, so stale tables are rebuilt in one
// transaction. Missing tables are left to AutoMigrate.
func ensureReleaseSubscriberIdentityKeys(db *gorm.DB) error {
	needsState, needsConnection := false, false
	for table, expected := range releaseSubscriberPrimaryKeys {
		actual, exists, err := releaseSubscriberTablePrimaryKey(db, table)
		if err != nil {
			return err
		}
		if !exists || slices.Equal(actual, expected) {
			continue
		}
		switch table {
		case "release_subscriber_states":
			needsState = true
		case "release_subscriber_connections":
			needsConnection = true
		}
	}
	if !needsState && !needsConnection {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if needsState {
			if err := rebuildReleaseSubscriberStates(tx); err != nil {
				return err
			}
		}
		if needsConnection {
			if err := rebuildReleaseSubscriberConnections(tx); err != nil {
				return err
			}
		}
		return nil
	})
}

// rebuildReleaseSubscriberStates rewrites a pre-v3 table with the current
// primary key. The source table predates the v7 divergence columns, so they
// are created with their defaults rather than copied; a future rebuild of a
// v7 table must extend the INSERT/SELECT to carry them.
func rebuildReleaseSubscriberStates(tx *gorm.DB) error {
	statements := []string{
		`CREATE TABLE release_subscriber_states_v3 (
			namespace_id integer NOT NULL, release_name text NOT NULL,
			client_name text NOT NULL, instance_id text NOT NULL,
			identity text NOT NULL DEFAULT "", state text NOT NULL,
			release_version integer NOT NULL, activation_revision integer NOT NULL,
			rejection_category text NOT NULL DEFAULT "", diagnostic text NOT NULL DEFAULT "",
			client_timestamp text NOT NULL, server_timestamp text NOT NULL,
			connected integer NOT NULL DEFAULT 0, disconnected_at text,
			applied_divergent integer NOT NULL DEFAULT 0, divergent_field_count integer NOT NULL DEFAULT 0,
			PRIMARY KEY (namespace_id,release_name,client_name,instance_id,identity,state),
			CONSTRAINT fk_release_subscriber_states_namespace FOREIGN KEY (namespace_id) REFERENCES namespaces(id))`,
		`INSERT INTO release_subscriber_states_v3 (
			namespace_id, release_name, client_name, instance_id, identity, state,
			release_version, activation_revision, rejection_category, diagnostic,
			client_timestamp, server_timestamp, connected, disconnected_at
		) SELECT namespace_id, release_name, client_name, instance_id, identity, state,
			release_version, activation_revision, rejection_category, diagnostic,
			client_timestamp, server_timestamp, connected, disconnected_at
		FROM release_subscriber_states`,
		`DROP TABLE release_subscriber_states`,
		`ALTER TABLE release_subscriber_states_v3 RENAME TO release_subscriber_states`,
		`CREATE INDEX idx_release_subscriber_disconnected ON release_subscriber_states(disconnected_at)`,
		`CREATE INDEX idx_release_subscriber_server_time ON release_subscriber_states(server_timestamp)`,
		`CREATE INDEX idx_release_subscriber_page ON release_subscriber_states(namespace_id, release_name, server_timestamp)`,
	}
	return execReleaseSubscriberMigration(tx, "states", statements)
}

func rebuildReleaseSubscriberConnections(tx *gorm.DB) error {
	statements := []string{
		`CREATE TABLE release_subscriber_connections_v3 (
			namespace_id integer NOT NULL, release_name text NOT NULL,
			client_name text NOT NULL, instance_id text NOT NULL,
			identity text NOT NULL DEFAULT "", connection_id text NOT NULL DEFAULT "",
			connected integer NOT NULL DEFAULT 0, connected_at text NOT NULL,
			disconnected_at text, server_timestamp text NOT NULL,
			PRIMARY KEY (namespace_id,release_name,client_name,instance_id,identity),
			CONSTRAINT fk_release_subscriber_connections_namespace FOREIGN KEY (namespace_id) REFERENCES namespaces(id))`,
		`INSERT INTO release_subscriber_connections_v3 (
			namespace_id, release_name, client_name, instance_id, identity,
			connection_id, connected, connected_at, disconnected_at, server_timestamp
		) SELECT namespace_id, release_name, client_name, instance_id, identity,
			connection_id, connected, connected_at, disconnected_at, server_timestamp
		FROM release_subscriber_connections`,
		`DROP TABLE release_subscriber_connections`,
		`ALTER TABLE release_subscriber_connections_v3 RENAME TO release_subscriber_connections`,
		`CREATE INDEX idx_release_connection_server_time ON release_subscriber_connections(server_timestamp)`,
		`CREATE INDEX idx_release_connection_disconnected ON release_subscriber_connections(disconnected_at)`,
		`CREATE INDEX idx_release_connection_page ON release_subscriber_connections(namespace_id, release_name, server_timestamp)`,
	}
	return execReleaseSubscriberMigration(tx, "connections", statements)
}

func execReleaseSubscriberMigration(tx *gorm.DB, table string, statements []string) error {
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("migrate release subscriber %s identity key: %w", table, err)
		}
	}
	return nil
}

func verifyReleaseSubscriberIdentityKeys(db *gorm.DB) error {
	for table, expected := range releaseSubscriberPrimaryKeys {
		actual, exists, err := releaseSubscriberTablePrimaryKey(db, table)
		if err != nil {
			return err
		}
		if !exists || !slices.Equal(actual, expected) {
			return fmt.Errorf("%s primary key = %v, want %v", table, actual, expected)
		}
	}
	return nil
}
