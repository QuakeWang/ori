package doris

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// Conn is the minimal Doris client contract used by tool handlers.
type Conn interface {
	Ping(ctx context.Context) (PingInfo, error)
	Query(ctx context.Context, query string) (*ResultSet, error)
	// SessionQuery executes setup statements (e.g. SET) and then the main query
	// within a single database connection so session variables are preserved.
	SessionQuery(ctx context.Context, setupSQLs []string, query string) (*ResultSet, error)
	Close() error
}

// PingInfo contains basic connection metadata.
type PingInfo struct {
	User     string
	Database string
	Version  string
}

// ResultSet preserves query column order and row values.
type ResultSet struct {
	Columns []string
	Rows    []map[string]string
}

// Client is a database/sql-backed Doris client.
type Client struct {
	cfg Config
	db  *sql.DB
}

// NewClient creates a Doris client for the given config.
func NewClient(cfg Config) (*Client, error) {
	driverCfg := mysql.NewConfig()
	driverCfg.Net = "tcp"
	driverCfg.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	driverCfg.User = cfg.User
	driverCfg.Passwd = cfg.Password
	driverCfg.DBName = strings.TrimSpace(cfg.Database)
	driverCfg.Params = map[string]string{"charset": "utf8mb4"}
	driverCfg.Timeout = cfg.ConnectTimeout
	driverCfg.ReadTimeout = cfg.QueryTimeout
	driverCfg.WriteTimeout = cfg.QueryTimeout

	db, err := sql.Open("mysql", driverCfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open doris connection: %w", err)
	}

	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(5 * time.Minute)

	return &Client{
		cfg: cfg,
		db:  db,
	}, nil
}

// Open creates a Conn using the default sql-backed client.
func Open(cfg Config) (Conn, error) {
	return NewClient(cfg)
}

// Ping verifies connectivity and returns basic cluster info.
func (c *Client) Ping(ctx context.Context) (PingInfo, error) {
	queryCtx, cancel := context.WithTimeout(ctx, c.cfg.QueryTimeout)
	defer cancel()

	var user sql.NullString
	var database sql.NullString
	var version sql.NullString
	row := c.db.QueryRowContext(queryCtx, "SELECT CURRENT_USER() AS user, DATABASE() AS db, VERSION() AS version")
	if err := row.Scan(&user, &database, &version); err != nil {
		return PingInfo{}, err
	}

	info := PingInfo{
		User:     valueOrEmpty(user),
		Database: valueOrEmpty(database),
		Version:  valueOrEmpty(version),
	}
	return info, nil
}

// Query executes a read-only Doris query and returns rows in column order.
func (c *Client) Query(ctx context.Context, query string) (*ResultSet, error) {
	queryCtx, cancel := context.WithTimeout(ctx, c.cfg.QueryTimeout)
	defer cancel()

	rows, err := c.db.QueryContext(queryCtx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanRows(rows)
}

// SessionQuery executes setup statements and the main query on a single connection.
func (c *Client) SessionQuery(ctx context.Context, setupSQLs []string, query string) (*ResultSet, error) {
	queryCtx, cancel := context.WithTimeout(ctx, c.cfg.QueryTimeout)
	defer cancel()

	conn, err := c.db.Conn(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("acquire session connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Execute setup statements (SET ...) first.
	for _, setup := range setupSQLs {
		if _, err := conn.ExecContext(queryCtx, setup); err != nil {
			return nil, fmt.Errorf("setup sql %q: %w", setup, err)
		}
	}

	// Execute the main query on the same connection.
	rows, err := conn.QueryContext(queryCtx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanRows(rows)
}

// scanRows converts sql.Rows into a ResultSet preserving column order.
func scanRows(rows *sql.Rows) (*ResultSet, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	result := &ResultSet{Columns: columns}
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}

		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}

		row := make(map[string]string, len(columns))
		for i, column := range columns {
			row[column] = stringifyValue(values[i])
		}
		result.Rows = append(result.Rows, row)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Close releases the underlying sql.DB.
func (c *Client) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func stringifyValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case []byte:
		return string(v)
	case time.Time:
		return v.Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(v)
	}
}

func valueOrEmpty(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}
