package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const appName = "catena"

// appVersion is replaced at release build time with -X main.appVersion=<version>.
var appVersion = "dev"

var (
	cfgFile         string
	dbPath          string
	port            int
	host            string
	debug           bool
	apiKey          string
	readOnly        bool
	corsOrigin      string
	bodyLimitBytes  int64
	queryTimeout    time.Duration
	rateLimitPerMin int
	maxRows         int
	backupDir       string
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "catena",
	Short: "Catena is a lightweight SQLite-over-HTTP database server",
	Long: `Catena is a single-binary server that turns any SQLite database file 
into a real-time HTTP and WebSocket accessible database. It handles 
concurrent operations safely using SQLite WAL mode and a serialized write queue.`,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print Catena version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("%s %s\n", appName, appVersion)
	},
}

var initConfigCmd = &cobra.Command{
	Use:   "init-config",
	Short: "Write a starter catena.yaml configuration file",
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := cmd.Flags().GetString("output")
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists", path)
		}
		return os.WriteFile(path, []byte(defaultConfigYAML), 0644)
	},
}

var inspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Inspect a SQLite database file",
	RunE: func(cmd *cobra.Command, args []string) error {
		setupLogger()
		db, err := OpenDB(dbPath, nil)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		info, err := db.Inspect(ctx)
		if err != nil {
			return err
		}
		payload, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(payload))
		return nil
	},
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Catena HTTP and WebSocket server",
	Run: func(cmd *cobra.Command, args []string) {
		setupLogger()

		// Print banner
		fmt.Println("  ____      _                     ")
		fmt.Println(" / ___|__ _| |_ ___ _ __   __ _ ")
		fmt.Println("| |   / _` | __/ _ \\ '_ \\ / _` |")
		fmt.Println("| |__| (_| | ||  __/ | | | (_| |")
		fmt.Println(" \\____\\__,_|\\__\\___|_| |_|\\__,_|")
		fmt.Println(" Lightest SQLite over HTTP Server")

		// Initialize WebSocket Hub
		hub := NewHub()
		go hub.Run()

		// Open SQLite Database. Read-only mode is enforced by SQLite itself.
		slog.Info("Opening SQLite database", "path", dbPath, "readonly", readOnly)
		var db *DB
		var err error
		if readOnly {
			db, err = OpenDBReadOnly(dbPath)
		} else {
			db, err = OpenDB(dbPath, hub.Broadcast)
		}
		if err != nil {
			slog.Error("Failed to open database", "err", err)
			os.Exit(1)
		}
		defer func() {
			slog.Info("Closing database connection pool")
			db.Close()
		}()

		// Initialize Server
		srv := NewServer(db, hub, ServerConfig{
			APIKey:          apiKey,
			ReadOnly:        readOnly,
			CORSOrigin:      corsOrigin,
			BodyLimitBytes:  bodyLimitBytes,
			QueryTimeout:    queryTimeout,
			RateLimitPerMin: rateLimitPerMin,
			MaxRows:         maxRows,
			BackupDir:       backupDir,
		})

		addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))

		// Set up context for graceful shutdown
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		// Start HTTP Server
		go func() {
			if err := srv.Start(addr); err != nil {
				slog.Error("Server error", "err", err)
				stop()
			}
		}()

		slog.Info("Catena is ready for connections", "url", fmt.Sprintf("http://%s", addr))

		// Wait for interruption signal
		<-ctx.Done()
		slog.Info("Shutting down Catena server gracefully...")

		// Allow connections to drain
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("Failed to stop HTTP server cleanly", "err", err)
		}

		slog.Info("Shutdown complete. Goodbye!")
	},
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./catena.yaml)")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable debug logging")

	serveCmd.Flags().StringVarP(&dbPath, "db", "d", "catena.db", "SQLite database file path")
	serveCmd.Flags().IntVarP(&port, "port", "p", 8080, "port to listen on")
	serveCmd.Flags().StringVar(&host, "host", "0.0.0.0", "host interface to bind to")
	serveCmd.Flags().StringVar(&apiKey, "api-key", "", "API key required for /query, /transaction and /ws")
	serveCmd.Flags().BoolVar(&readOnly, "readonly", false, "allow only read queries")
	serveCmd.Flags().StringVar(&corsOrigin, "cors-origin", "*", "Access-Control-Allow-Origin value")
	serveCmd.Flags().Int64Var(&bodyLimitBytes, "body-limit", 1<<20, "maximum JSON request body size in bytes")
	serveCmd.Flags().DurationVar(&queryTimeout, "query-timeout", 30*time.Second, "maximum duration for each query request")
	serveCmd.Flags().IntVar(&rateLimitPerMin, "rate-limit", 0, "per-client request limit per minute; 0 disables rate limiting")
	serveCmd.Flags().IntVar(&maxRows, "max-rows", 10000, "maximum rows returned by one query")
	serveCmd.Flags().StringVar(&backupDir, "backup-dir", "backups", "directory for database backups")
	inspectCmd.Flags().StringVarP(&dbPath, "db", "d", "catena.db", "SQLite database file path")
	inspectCmd.Flags().DurationVar(&queryTimeout, "query-timeout", 30*time.Second, "maximum duration for inspection queries")
	initConfigCmd.Flags().String("output", "catena.yaml", "configuration file path to create")

	// Bind cobra flags to viper
	viper.BindPFlag("db", serveCmd.Flags().Lookup("db"))
	viper.BindPFlag("port", serveCmd.Flags().Lookup("port"))
	viper.BindPFlag("host", serveCmd.Flags().Lookup("host"))
	viper.BindPFlag("api_key", serveCmd.Flags().Lookup("api-key"))
	viper.BindPFlag("readonly", serveCmd.Flags().Lookup("readonly"))
	viper.BindPFlag("cors_origin", serveCmd.Flags().Lookup("cors-origin"))
	viper.BindPFlag("body_limit", serveCmd.Flags().Lookup("body-limit"))
	viper.BindPFlag("query_timeout", serveCmd.Flags().Lookup("query-timeout"))
	viper.BindPFlag("rate_limit", serveCmd.Flags().Lookup("rate-limit"))
	viper.BindPFlag("max_rows", serveCmd.Flags().Lookup("max-rows"))
	viper.BindPFlag("backup_dir", serveCmd.Flags().Lookup("backup-dir"))

	rootCmd.AddCommand(serveCmd, versionCmd, initConfigCmd, inspectCmd)
}

const defaultConfigYAML = `db: "catena.db"
host: "0.0.0.0"
port: 8080
api_key: "change-me"
readonly: false
cors_origin: "*"
body_limit: 1048576
query_timeout: "30s"
rate_limit: 0
max_rows: 10000
backup_dir: "backups"
`

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		// Search config in current directory with name "catena" (without extension)
		viper.AddConfigPath(".")
		viper.SetConfigType("yaml")
		viper.SetConfigName("catena")
	}

	viper.SetEnvPrefix("CATENA")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		slog.Info("Using config file", "file", viper.ConfigFileUsed())
	}

	applyConfig()
}

func applyConfig() {
	if viper.IsSet("db") {
		dbPath = viper.GetString("db")
	}
	if viper.IsSet("port") {
		port = viper.GetInt("port")
	}
	if viper.IsSet("host") {
		host = viper.GetString("host")
	}
	if viper.IsSet("api_key") {
		apiKey = viper.GetString("api_key")
	}
	if viper.IsSet("readonly") {
		readOnly = viper.GetBool("readonly")
	}
	if viper.IsSet("cors_origin") {
		corsOrigin = viper.GetString("cors_origin")
	}
	if viper.IsSet("body_limit") {
		bodyLimitBytes = viper.GetInt64("body_limit")
	}
	if viper.IsSet("query_timeout") {
		queryTimeout = viper.GetDuration("query_timeout")
	}
	if viper.IsSet("rate_limit") {
		rateLimitPerMin = viper.GetInt("rate_limit")
	}
	if viper.IsSet("max_rows") {
		maxRows = viper.GetInt("max_rows")
	}
	if viper.IsSet("backup_dir") {
		backupDir = viper.GetString("backup_dir")
	}
}

func setupLogger() {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)
}
