package doris

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuakeWang/ori/internal/envutil"
)

const (
	defaultHost                  = "127.0.0.1"
	defaultPort                  = 9030
	defaultHTTPPort              = 8030
	defaultUser                  = "root"
	defaultConnectTimeoutSeconds = 10
	defaultQueryTimeoutSeconds   = 30
)

// Config describes the Doris FE connection settings.
type Config struct {
	Host           string
	Port           int
	HTTPPort       int
	User           string
	Password       string
	Database       string
	ConnectTimeout time.Duration
	QueryTimeout   time.Duration
}

// LoadConfig reads Doris connection settings from the workspace .env and the
// current process environment, without mutating os.Environ().
// When workspace is non-empty, it reads workspace/.env; otherwise it falls
// back to ".env" relative to the process working directory.
// Existing process env values win over .env values.
func LoadConfig(workspace string) (Config, error) {
	env, err := envutil.Snapshot(workspace)
	if err != nil {
		return Config{}, err
	}

	port, err := envutil.IntOr(env, "DORIS_FE_PORT", defaultPort)
	if err != nil {
		return Config{}, err
	}
	httpPort, err := envutil.IntOr(env, "DORIS_FE_HTTP_PORT", defaultHTTPPort)
	if err != nil {
		return Config{}, err
	}
	connectTimeout, err := envutil.IntOr(env, "DORIS_CONNECT_TIMEOUT", defaultConnectTimeoutSeconds)
	if err != nil {
		return Config{}, err
	}
	queryTimeout, err := envutil.IntOr(env, "DORIS_QUERY_TIMEOUT", defaultQueryTimeoutSeconds)
	if err != nil {
		return Config{}, err
	}
	if port <= 0 {
		return Config{}, fmt.Errorf("invalid DORIS_FE_PORT %d: must be > 0", port)
	}
	if httpPort <= 0 {
		return Config{}, fmt.Errorf("invalid DORIS_FE_HTTP_PORT %d: must be > 0", httpPort)
	}
	if connectTimeout <= 0 {
		return Config{}, fmt.Errorf("invalid DORIS_CONNECT_TIMEOUT %d: must be > 0", connectTimeout)
	}
	if queryTimeout <= 0 {
		return Config{}, fmt.Errorf("invalid DORIS_QUERY_TIMEOUT %d: must be > 0", queryTimeout)
	}

	return Config{
		Host:           envutil.StringOr(env, "DORIS_FE_HOST", defaultHost),
		Port:           port,
		HTTPPort:       httpPort,
		User:           envutil.StringOr(env, "DORIS_USER", defaultUser),
		Password:       env["DORIS_PASSWORD"],
		Database:       env["DORIS_DATABASE"],
		ConnectTimeout: time.Duration(connectTimeout) * time.Second,
		QueryTimeout:   time.Duration(queryTimeout) * time.Second,
	}, nil
}

// LoadConfigWithDatabase loads Doris connection settings and applies an optional
// database override for the current request.
func LoadConfigWithDatabase(workspace, database string) (Config, error) {
	cfg, err := LoadConfig(workspace)
	if err != nil {
		return Config{}, err
	}
	if db := strings.TrimSpace(database); db != "" {
		cfg.Database = db
	}
	return cfg, nil
}
