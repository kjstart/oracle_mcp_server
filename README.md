[English](#english) | [中文](#chinese)

<a id="english"></a>

# New Project: Cursor DB MCP  
Supports connecting to any database via JDBC.  
👉[https://github.com/kjstart/cursor_db_mcp](https://github.com/kjstart/cursor_db_mcp)

---
# Oracle MCP Server

A Model Context Protocol (MCP) server for Oracle Database, enabling AI assistants like Cursor to execute SQL statements directly against Oracle databases. Oracle trademark, database, and Instant Client are properties of the Oracle company, they are not part of this solution.

**Alvin Liu** — [https://alvinliu.com](https://alvinliu.com) · **Project:** [https://github.com/kjstart/cursor_oracle_mcp_server](https://github.com/kjstart/cursor_oracle_mcp_server)

## 🎬 Demo Video
👉 Click the image below to watch on YouTube
[![Cursor Oracle MCP Demo](https://www.alvinliu.com/wp-content/uploads/2026/02/oracle_mcp_youtube.png)](https://www.youtube.com/watch?v=3U1nWj9tP24)

## Features

v2.0.2 fixes concurrent confirmation-file collisions, uses a native full-text view for large Windows SQL, adds request/UI/connection stage diagnostics, and removes obsolete agent guidance to list connections for recovery.

- **v2.0.0 lazy connections**: `oracle.lazy_mode: true` by default; startup and connection listing open no SSH or database connections. Every database operation checks/reconnects its selected connection before SQL is sent.
- **Independent bastion tunnels**: One SSH login to each connection's bastion, then TCP forwarding to the Oracle listener; no database-server SSH account/key. Connectivity diagnostics go to a separate `error.log`.

- **Full SQL support**: SELECT, INSERT, UPDATE, DELETE, DDL (CREATE, DROP, ALTER, etc.), and multiple statements per request
- **Execute from file**: Run a full SQL file via `execute_sql_file`; trailing SQL*Plus `/` is stripped automatically
- **Query to file**: `query_to_csv_file` (result as CSV, RFC 4180, UTF-8) and `query_to_text_file` (plain text, tab-separated, CLOB in full; e.g. for procedure source)
- **PL/SQL blocks**: CREATE PROCEDURE/FUNCTION/PACKAGE (including files with leading comments) and anonymous blocks are executed as one unit
- **Human-in-the-loop review UI**: Configurable danger keywords trigger a review window with full SQL. The `Keywords` area shows the actual expanded identifiers that triggered review (for example `created_at`, not just `create`)
- **Whitelist review bypass**: `Allow Header` saves the SQL first line to `whitelist.json` and skips future review for the same connection + header line
- **Whitelist keyword filtering**: `Allow Keyword` saves a keyword to `whitelist.json` without approving the current review; future matches remove only that specific expanded keyword trigger, while other unmatched dangerous triggers still open review
- **Whitelist keyword guardrails**: review UI only accepts whitelist keywords made of letters, numbers, and underscores, and blocks values that exactly match `danger_keywords` (case-insensitive)
- **Danger keyword matching**: `whole_text` (whole-word matching including strings/comments) or `tokens` (exact tokens after stripping strings/comments); identifiers such as `request_delete` do not match `delete`
- **Multi-database**: Configure multiple connections; default lazy mode lists configured names without opening connections. Every database operation checks/reconnects its selected connection. Set `oracle.lazy_mode: false` for eager connections
- **Audit logging**: Keyed fields (`AUDIT_TIME`, `AUDIT_CONNECTION`, `AUDIT_KEYWORDS`, `AUDIT_APPROVED`, `AUDIT_ACTION`, `AUDIT_SQL`) plus whitelist context such as `HEADER_LINE` and `EXPANDED_KEYWORDS`; full SQL, record separator `######AUDIT_END######`; 10MB rotation, reuse last non-full file on startup, filenames include creation date (e.g. `audit_2006-01-02_150405.log`)
- **Connection security**: Database connection strings (including passwords) in `config.yaml` are automatically encrypted on first startup — no manual steps required
- **Cross-platform review UI**: Windows (WinForms + WebBrowser) and macOS (JXA/Cocoa dialog) both support `Allow Keyword`, `Allow Header`, `Execute`, and `Cancel`
- **Single executable**: Standalone binary (requires Oracle Instant Client)

<img src="https://www.alvinliu.com/wp-content/uploads/2026/04/db_mcp_whilelist.png" alt="db_mcp confirmation window" />

## Requirements

### Runtime Dependencies

1. **Oracle Instant Client**
   - Download from [Oracle website](https://www.oracle.com/database/technologies/instant-client/downloads.html)
   - Version 19c or later recommended
   - Required files: `oci.dll`, `oraociei19.dll` (Windows) or equivalent `.dylib` (macOS)

2. **Environment Setup**
   ```bash
   # Windows: Add Instant Client to PATH
   set PATH=C:\path\to\instantclient;%PATH%
   
   # macOS: Set library path
   export DYLD_LIBRARY_PATH=/path/to/instantclient:$DYLD_LIBRARY_PATH
   ```

## Installation

### Download pre-built binary (recommended)

Pre-built binaries are published on [GitHub Releases](https://github.com/kjstart/cursor_oracle_mcp_server/releases). No build required.

1. **Download** the archive for your platform:
   - **Windows**: `oracle-mcp-server-windows-amd64-<version>.zip`
   - **macOS Apple Silicon (M1/M2/M3)**: `oracle-mcp-server-darwin-arm64-<version>.tar.gz`
   - **macOS Intel**: `oracle-mcp-server-darwin-amd64-<version>.tar.gz`

2. **Extract** the archive. You will get:
   - The executable (e.g. `oracle-mcp-server-windows-amd64.exe` or `oracle-mcp-server-darwin-arm64`)
   - `user_guide.md` — step-by-step setup
   - `config.yaml.example` — copy to `config.yaml` and edit

3. **Install Oracle Instant Client** (see [Requirements](#requirements) above) and add it to `PATH` (Windows) or set `ORACLE_HOME` and `DYLD_LIBRARY_PATH` (macOS).

4. **Configure** `config.yaml` with your database connection(s), then add the server to Cursor MCP (see [Configuration](#configuration) and [Usage with Cursor](#usage-with-cursor) below).

For a full walkthrough, see [user_guide.md](user_guide.md).

## Configuration

Copy `config.yaml.example` to `config.yaml` and configure at least one connection under `oracle.connections`:

```yaml
oracle:
  connections:
    database1: "user/pass@//host:1521/ORCL"
    # database2: "user/pass@//host2:1521/ORCL"

security:
  # "whole_text" = whole words including strings/comments; "tokens" = tokens without strings/comments
  danger_keyword_match: "whole_text"

  danger_keywords:
    - truncate
    - drop
    - delete
    - create
    - update
    - execute immediate
    # ... (see config.yaml.example)

  require_confirm_for_ddl: true   # DDL always requires confirmation

logging:
  audit_log: true
  verbose_logging: true   # One short stderr line per execute_sql / execute_sql_file
  log_file: "audit.log"  # Base name; actual files: audit_YYYY-MM-DD_HHMMSS.log, 10MB rotation
```

Always pass the configured `connection` name to database tools, including when only one connection is configured. Reuse connection names already supplied by the user or known in the conversation. Call `list_connections` only to discover unknown names or inspect last-known diagnostics when requested.

**Multiple security profiles:** define named profiles under `security_profiles` (same fields as `security`; omitted fields use built-in defaults) and bind connections to them with `oracle.connection_security`. Connections not listed use the top-level `security` section (profile name `default`, which can also be referenced explicitly). Unknown profile or connection names are rejected at startup.

```yaml
oracle:
  connections:
    prod: "user/pass@//host:1521/PROD"
    dev: "user/pass@//host:1521/DEV"
  connection_security:
    prod: strict
security_profiles:
  strict:
    danger_keyword_match: "tokens"
    danger_keywords: [drop, delete, update, insert]
    require_confirm_for_ddl: true
```

> **Connection security:** On first startup, any plain-text connection strings in `config.yaml` are automatically replaced with encrypted values. The file is updated in place — comments and formatting are preserved.

### Per-connection SSH tunnels

Add an optional `oracle.connection_ssh` entry keyed by database connection name:

```yaml
oracle:
  connections:
    prod: 'user="app" password="db-password" connectString="db.internal:1521/ORCLPDB1"'
    dev: 'app/password@//dev-db:1521/DEV'
  connection_ssh:
    prod:
      host: jump.example.com
      port: 22
      username: oracle
      private_key_file: '~/.ssh/id_ed25519'
      remote_host: db.internal
      remote_port: 1521
logging:
  error_log_file: error.log
```

Each configured connection owns its SSH session and loopback listener, even when jump hosts are identical. Connections without SSH settings remain direct. SSH settings remain **plaintext**, including optional `password` and `private_key_passphrase`; database DSNs retain their existing encryption. Choose exactly one authentication method: `private_key_file`, `password`, or `password_env`. For encrypted keys, use either `private_key_passphrase` or `private_key_passphrase_env`. Environment fields contain variable names. Relative file paths resolve from the config directory; `~/` resolves from the user's home directory.

`host`, `port`, `username` and SSH authentication fields describe the **bastion only**. The program performs one SSH login to that bastion, then requests TCP forwarding to `remote_host:remote_port` (the Oracle listener). The database server needs no SSH service, operating-system account or SSH key. The application does not import `~/.ssh/config` or execute ProxyCommand programs.

The SSH host must already be trusted in `known_hosts` (default `~/.ssh/known_hosts`); verify its fingerprint before adding it. The jump host must permit TCP forwarding and reach `remote_host:remote_port`. That target is resolved from the jump host. Defaults: SSH port 22, remote port 1521. Local ports are automatically allocated and kept stable during SSH reconnects. Runtime DSN changes are never saved to disk.

`known_hosts_file` is optional; normally omit it to use the runtime user's `~/.ssh/known_hosts` on Windows and macOS. `host` is the bastion SSH address; `remote_host` is the database TCP address as reached from that bastion. Only the bastion needs an SSH key.

`oracle.lazy_mode` defaults to **true**. Startup and `list_connections` do not connect to SSH or Oracle, and no background recovery workers are started. Every database operation checks/reconnects only its selected connection before executing SQL: verify the bastion SSH session with a round trip, then ping the database and recreate a failed connection pool when needed. New connections perform the SSH handshake and database connection test. With `lazy_mode: false`, startup connects eagerly, listing retries failures, and SSH recovery also runs in the background with backoff up to 60 seconds. Pre-operation checks apply in both modes. In-flight SQL may fail and is **never replayed**. Shutdown closes connection pools, SSH sessions, listeners and forwarding connections.

Normally one tool call is sufficient even after an idle tunnel disconnects: MCP reconnects internally **before sending SQL**, then executes the requested operation. If connection setup fails, fix the reported cause and call the same operation again; no `list_connections` call is required. If SQL was already submitted when the connection broke, first check whether a write completed before manually repeating it.

`list_connections` returns `lazy_mode` and includes `connection_checked`, `ssh_enabled`, `ssh_connected`, `database_connected`, `error_code`, `last_error`, and `user_action`, alongside `name` and `available`. In lazy mode every configured connection has `available: true` (configured for use, **not verified reachable**). Other fields are last-known diagnostics: an unused connection has `connection_checked: false`. Previously observed failures remain visible without causing a retry during listing. SQL tool errors also report an SSH cause when known. Error codes distinguish `SSH_CONFIG_ERROR`, `SSH_CONNECT_FAILED`, `SSH_DISCONNECTED`, `SSH_FORWARD_FAILED`, `SSH_LISTEN_FAILED`, `SSH_DSN_UNSUPPORTED`, and `DATABASE_CONNECT_FAILED`. AI clients should use `user_action` to help users check the appropriate settings.

Agent instructions should reuse a connection name already supplied by the user or known in the conversation. Use `list_connections` only to discover unknown names or inspect last-known diagnostics when explicitly needed. Remove older prompts that require listing before each query or after a query failure to reconnect. Recovery runs inside the database operation; default lazy listing cannot reconnect anything. Changing the MCP's tool descriptions does not automatically update separately maintained client prompts or already cached tool metadata.

Connectivity failures go to `logging.error_log_file` (default `error.log`, relative to the config directory) and stderr, independently of audit logging. Repeated identical failures are suppressed until their state changes. SQL audit records retain attempted operations but omit SSH diagnostic details. Database passwords and DSNs are not included in connectivity diagnostics.

Confirmation dialogs use a separate PID/random temporary directory for every request, so concurrent Codex/Claude MCP processes cannot overwrite each other's SQL or approval result. Windows SQL larger than 64 KiB or at least 1,000 newline characters uses a read-only native text view with the full SQL; smaller statements keep syntax highlighting. Temporary files are removed after the dialog exits.

The same diagnostic log records request/confirmation/connection/SQL stages with MCP PID, request ID, connection name and elapsed milliseconds. Windows dialogs emit content-ready, shown, button and heartbeat signals; while awaiting a decision, the parent logs `CONFIRM_WAIT` every 10 seconds with the last UI signal age. Timestamps are explicitly marked UTC. SQL text is excluded from these stage logs. Process failures and missing/invalid approval results return a confirmation error instead of being classified as user cancellation. Waiting for the user has no approval deadline and never auto-approves.

SSH currently supports a **single TCP Easy Connect** database DSN (`host:port/service`). TNS aliases, TCPS/wallets, multi-host descriptors and RAC/SCAN redirection are not supported. The Oracle Instant Client requirement remains unchanged.

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ORACLE_MCP_CONFIG` | Path to config file (overrides default locations) |
| `ORACLE_HOME` | Oracle client installation path |
| `PATH` (Windows) | Must include Instant Client directory |
| `TNS_ADMIN` | **Required for Oracle Autonomous Database (ADB)** — directory containing `tnsnames.ora` and wallet files (e.g. `cwallet.sso`, `ewallet.pem`, `sqlnet.ora`) from the ADB Wallet zip |

### Oracle Autonomous Database (ADB) with Wallet

ADB uses **TCPS (SSL)** and requires the **Wallet**. Use the TNS name from the wallet’s `tnsnames.ora` (e.g. `mcpdemo_high`):

1. **Download Wallet**: Oracle Cloud Console → your Autonomous Database → **DB connection** → **Download Wallet**. Unzip to a folder (e.g. `D:\oracle\wallet_mcpdemo`). The folder must contain `tnsnames.ora`, `sqlnet.ora`, `cwallet.sso`, `ewallet.pem`, etc.
2. **config.yaml** — use TNS alias and your DB user/password:
   ```yaml
   oracle:
     connections:
       mcpdemo: "mcpdemo/YourPassword@mcpdemo_high"
   ```
3. **Cursor MCP** — set `TNS_ADMIN` to the **Wallet directory** so the process can find `tnsnames.ora` and SSL certs. On Windows, also include Instant Client in `PATH`:
   ```json
   {
     "mcpServers": {
       "oracle": {
         "command": "D:\\work\\code\\cursor_oracle_mcp_server\\oracle-mcp.exe",
         "args": [],
         "env": {
           "TNS_ADMIN": "D:\\oracle\\wallet_mcpdemo",
           "PATH": "C:\\path\\to\\instantclient;%PATH%"
         }
       }
     }
   }
   ```
   Replace `D:\oracle\wallet_mcpdemo` with your unzipped wallet path, and ensure Instant Client is on `PATH`. Without `TNS_ADMIN`, you may see **ORA-12541** (no listener) or SSL errors because the client cannot resolve the TNS name or use the wallet.

## Usage with Cursor

### MCP Configuration

Add to your Cursor MCP settings (`~/.cursor/mcp.json` or workspace `.cursor/mcp.json`):

**Windows:**

#### Windows, add Oracle client to PATH
```json
{
  "mcpServers": {
    "oracle": {
      "command": "C:\\path\\to\\oracle-mcp.exe",
      "args": []
    }
  }
}
```

#### Mac

Use the `env` block so the MCP process sees `ORACLE_HOME` and `DYLD_LIBRARY_PATH`:

```json
{
  "mcpServers": {
    "oracle": {
      "command": "/path/to/oracle-mcp",
      "args": [],
      "env": {
        "ORACLE_HOME": "/opt/oracle/instantclient_19_20",
        "DYLD_LIBRARY_PATH": "/opt/oracle/instantclient_19_20"
      }
    }
  }
}
```

Replace `/path/to/oracle-mcp` and `/opt/oracle/instantclient_19_20` with your actual paths. You can also reference existing shell env with `"ORACLE_HOME": "${env:ORACLE_HOME}"` if Cursor was started from a terminal that already has it set.

### Tools

| Tool | Description |
|------|-------------|
| **execute_sql** | Run SQL (one or multiple statements). Params: `sql`, required `connection`. Do not specify schema-qualified object names such as `hr.employees`; the server rejects them. |
| **execute_sql_file** | Read SQL from a file, analyze, show review if needed, then execute. Trailing `/` is stripped. Params: `file_path`, required `connection`. SQL in the file must not specify schema-qualified object names such as `hr.employees`. |
| **list_connections** | Default lazy mode lists configured connections without network I/O; `available: true` means configured. Every database operation checks/reconnects its selected connection. Eager mode also retries failed connections during listing. |
| **query_to_csv_file** | Run a query and write the result to a file as CSV (header + rows, UTF-8, RFC 4180). Params: `sql`, `file_path` (absolute), required `connection`. Same review as `execute_sql` when SQL matches danger keywords or DDL (if enabled). Do not specify schema-qualified object names such as `hr.employees`. |
| **query_to_text_file** | Run a query and write the result to a file as plain text (tab-separated, no header; CLOB in full; e.g. for procedure source). Params: `sql`, `file_path` (absolute), required `connection`. Same review as `execute_sql` when SQL matches danger keywords or DDL (if enabled). Do not specify schema-qualified object names such as `hr.employees`. |

### Example Interactions

```
// One connection: no need to pass connection
execute_sql({ "sql": "SELECT table_name FROM user_tables" })

// Multiple connections
execute_sql({ "sql": "SELECT * FROM my_table", "connection": "database1" })
execute_sql({ "sql": "CREATE TABLE test (id NUMBER)", "connection": "database2" })

// Run a SQL file (e.g. procedure script; trailing / stripped)
execute_sql_file({ "file_path": "d:\\scripts\\myscript.sql", "connection": "ps" })

// See connection names and status
list_connections()

// Write query result to CSV or text file (file_path must be absolute; same review rules as execute_sql when needed)
query_to_csv_file({ "sql": "SELECT * FROM my_table", "file_path": "d:\\out\\data.csv", "connection": "database1" })
query_to_text_file({ "sql": "SELECT text FROM user_source WHERE name='MY_PROC'", "file_path": "d:\\out\\my_proc.sql" })
```

## Safety and Review Window

When SQL matches `danger_keywords` or is DDL (if `require_confirm_for_ddl` is true), a confirmation window appears:

- **Keywords display**: the review header shows the actual expanded identifiers that triggered review, not just the original configured danger keyword
- **Allow Header**: stores the current SQL first line in `whitelist.json`; future SQL on the same connection with the same first line skips review
- **Allow Keyword**: stores one keyword in `whitelist.json` but does **not** approve the current review; users can add multiple keywords before choosing `Execute` or `Cancel`
- **Keyword whitelist behavior**: a whitelisted keyword removes only that specific expanded-keyword trigger; if the same SQL still has other dangerous unmatched triggers, review still opens
- **Whitelist storage**: `whitelist.json` is stored in the executable directory / program directory
- **Windows**: WinForms window with syntax-highlighted SQL (WebBrowser)
- **macOS**: JXA/Cocoa dialog with the same review actions as Windows

Execution proceeds only after the user confirms. Rejection is logged and returned as `USER_REJECTED`.

## SQL Execution

- **Single statement**: One SQL statement, with or without trailing semicolon.
- **Multiple statements**: One per line, **each line ending with a semicolon**. Executed in order.
- **PL/SQL**: CREATE PROCEDURE/FUNCTION/PACKAGE (including files with leading `--` or `/* */`) and anonymous blocks (BEGIN...END; / DECLARE...END;) are treated as one block and not split.
- **From file**: Trailing SQL*Plus `/` (on its own line) is removed before execution.

## Audit Log

- **Keyed format**: `AUDIT_TIME=...`, `AUDIT_CONNECTION=...`, `AUDIT_KEYWORDS=...`, `AUDIT_APPROVED=...`, `AUDIT_ACTION=...`, optional `HEADER_LINE=...`, optional `EXPANDED_KEYWORDS=...`, then `AUDIT_SQL=` followed by the full SQL, then a line `######AUDIT_END######` as record separator.
- **Rotation**: 10MB per file. On startup, the most recent existing log file under 10MB is reused; when full, a new file is created with creation date in the name: `audit_2006-01-02_150405.log`.

## MCP Protocol

### Tool: `execute_sql`

**Input**: `sql` (required), `connection` (required). Do not specify schema-qualified object names such as `hr.employees`; the server rejects them.

**Output (query)**: `columns`, `rows`, `statement_type`, `execution_time_ms`, `success`.

**Output (DML/DDL)**: `rows_affected`, `statement_type`, `execution_time_ms`, `success`, optional `warning`.

**Error (user rejected)**: `code` -32000, `message` "Execution cancelled by user", `data.code` "USER_REJECTED", `data.matched_keywords`.

### Tool: `execute_sql_file`

**Input**: `file_path` (required), `connection` (required). Same analysis and review rules as `execute_sql`; executes the file contents (trailing `/` stripped).

### Tool: `list_connections`

**Input**: none. **Output**: `lazy_mode`, `connections`, `message`. Each connection includes `name`, `available`, `connection_checked`, `ssh_enabled`, `ssh_connected`, `database_connected`, and optional `error_code`, `last_error`, `user_action`. Default lazy mode performs no network checks and returns all configured connections with `available: true`; diagnostics describe the last observed state. Database operations check/reconnect their selected connection before executing SQL.

### Tool: `query_to_csv_file`

**Input**: `sql` (required), `file_path` (required, absolute path), `connection` (required). **Output**: success and path. Same confirmation rules as `execute_sql` when SQL matches `danger_keywords` or is DDL (if `require_confirm_for_ddl`). Writes CSV with header, UTF-8, RFC 4180; CLOB columns read in full.

### Tool: `query_to_text_file`

**Input**: `sql` (required), `file_path` (required, absolute path), `connection` (required). **Output**: success and path. Same confirmation rules as `execute_sql` when SQL matches `danger_keywords` or is DDL (if `require_confirm_for_ddl`). Writes plain text, tab-separated columns, no header; CLOB in full (e.g. for procedure source).

## Troubleshooting

### Connection Issues

```
Error: ORA-12541: TNS:no listener
```
→ Check that Oracle Instant Client is in PATH and listener is running

```
Error: DPI-1047: Cannot locate a 64-bit Oracle Client library
```
→ Install Oracle Instant Client and add to PATH

### Permission Issues

```
Error: ORA-01031: insufficient privileges
```
→ The configured database user lacks required permissions

## Building from Source (optional)

If you prefer to build the binary yourself (e.g. for a different Go version or platform):

### Build dependencies

- Go 1.26+
- **CGO must be enabled**: godror requires CGO and a C compiler.
- **Windows**: Use **MinGW-w64** (GCC 7.2+) and add its `bin` to PATH.
  - **Do not use Cygwin's gcc.** If both are installed, ensure MinGW's `bin` comes before Cygwin in PATH, or you may see `cannot parse gcc output ... as ELF, Mach-O, PE, XCOFF`.
  - Install via Chocolatey: `choco install mingw`, or [MSYS2](https://www.msys2.org/) then `pacman -S mingw-w64-ucrt-x86_64-gcc` and use `mingw64\bin`.
  - Verify with `where gcc` and `gcc -v`; you should see "mingw" or "MinGW" and 64-bit (x86_64).
- **macOS**: Run `xcode-select --install` for command line tools.

### Build commands

**Important**: Build with CGO enabled and GCC available, or you will see errors like `undefined: VersionInfo`.

```bash
# Clone the repository
git clone https://github.com/kjstart/cursor_oracle_mcp_server
cd cursor_oracle_mcp_server

# Download dependencies
go mod tidy

# Windows (PowerShell): enable CGO and ensure gcc is in PATH
$env:CGO_ENABLED="1"
go build -o oracle-mcp.exe .

# Windows (CMD)
set CGO_ENABLED=1
go build -o oracle-mcp.exe .

# Windows (MSYS UCRT64)
export PATH=/c/oracle/instantclient_21_12:$PATH
export ORACLE_HOME=/c/oracle/instantclient_21_12
pacman -S mingw-w64-ucrt-x86_64-go
export GOROOT=/ucrt64/lib/go
export PATH=$GOROOT/bin:$PATH
export CGO_ENABLED=1
export CC=x86_64-w64-mingw32-gcc
go install github.com/godror/godror@latest
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -o oracle-mcp.exe .

# macOS (Intel): native build only
CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -o oracle-mcp .

# macOS (Apple Silicon)
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -o oracle-mcp .
```

### Build troubleshooting

| Error | Cause | Fix |
|-------|------|-----|
| `undefined: VersionInfo` | CGO disabled | Set `CGO_ENABLED=1` and install gcc |
| `gcc not found` | gcc not installed or not in PATH | Install MinGW-w64 and add its `bin` to PATH |
| `cannot parse gcc output ... as ELF, Mach-O, PE, XCOFF` | Wrong gcc (e.g. **Cygwin gcc**) | Use **MinGW-w64** gcc and put it before Cygwin in PATH; verify with `where gcc` and `gcc -v` |

After changing gcc or PATH, clear the build cache and rebuild:

```bash
go clean -cache
go build -o oracle-mcp.exe .
```

## License

MIT License - see [LICENSE](LICENSE) file

## Contributing

1. Fork the repository
2. Create a feature branch
3. Submit a pull request

## Acknowledgments

- [godror](https://github.com/godror/godror) - Oracle driver for Go
- [Model Context Protocol](https://modelcontextprotocol.io/) - MCP specification

---

<a id="chinese"></a>

[English](#english) | [中文](#chinese)

# 新项目 Cursor DB MCP 可以连接到各种数据库产品(包括OceanBase等信创数据库)
👉[https://github.com/kjstart/cursor_db_mcp](https://github.com/kjstart/cursor_db_mcp)

---
# Oracle MCP Server（中文）

基于 Model Context Protocol (MCP) 的 Oracle 数据库服务端，让 Cursor 等 AI 助手直接对 Oracle 数据库执行 SQL。Oracle 商标, 数据库和Instant Client属于Oracle公司, 不是本方案的一部分.

**作者：Alvin Liu** — [https://alvinliu.com](https://alvinliu.com) · **项目：** [https://github.com/kjstart/cursor_oracle_mcp_server](https://github.com/kjstart/cursor_oracle_mcp_server)

B站视频介绍: [Cursor连接Oracle自动编写存储过程](https://www.bilibili.com/video/BV1EbfLBREDC)

## 功能

- **完整 SQL 支持**：SELECT、INSERT、UPDATE、DELETE、DDL（CREATE、DROP、ALTER 等），单次请求可执行多条语句
- **从文件执行**：通过 `execute_sql_file` 执行整个 SQL 文件；自动去除末尾 SQL*Plus 的 `/`
- **查询结果写入文件**：`query_to_csv_file`（结果写为 CSV，RFC 4180，UTF-8）与 `query_to_text_file`（纯文本、制表符分隔、CLOB 完整输出，如存过程源码）
- **PL/SQL 块**：CREATE PROCEDURE/FUNCTION/PACKAGE（含文件头部注释）及匿名块作为整体执行
- **人工确认窗口**：可配置危险关键词，触发带完整 SQL 的确认窗口；`Keywords` 区域显示的是真正命中的 expanded keyword（例如显示 `created_at`，而不是只显示 `create`）
- **Header 白名单**：`Allow Header` 会把 SQL 第一行写入 `whitelist.json`，下次同一连接且第一行相同的 SQL 直接跳过 review
- **Keyword 白名单**：`Allow Keyword` 只把关键字写入 `whitelist.json`，不会自动批准当前 review；后续只会消掉该 expanded keyword 对应的触发项，其他未放行触发项仍会继续弹出 review
- **Keyword 白名单限制**：只能添加字母、数字、下划线组成的关键字；如果与 `danger_keywords` 中某项完全相同（忽略大小写）则禁止添加
- **危险词匹配**：`whole_text` 按完整词匹配（包括字符串和注释）；`tokens` 去掉字符串和注释后按 token 匹配。`request_delete` 等标识符不会因包含 `delete` 而命中。
- **v2.0.0 延迟连接**：`oracle.lazy_mode` 默认开启，启动及列出连接不建立隧道或数据库连接；每次数据库操作前仅检查并恢复所选连接。
- **独立 bastion 隧道**：每个连接独立配置 bastion，通过其转发数据库 TCP 端口；无需数据库服务器 SSH 账号或密钥，连接错误写入独立 `error.log`。
- **审计日志**：键值字段（`AUDIT_TIME`、`AUDIT_CONNECTION`、`AUDIT_KEYWORDS`、`AUDIT_APPROVED`、`AUDIT_ACTION`、`AUDIT_SQL`），并可附带 `HEADER_LINE`、`EXPANDED_KEYWORDS`；完整 SQL、记录分隔符 `######AUDIT_END######`；单文件 10MB 轮转，启动时复用最近未满的日志，文件名含创建日期（如 `audit_2006-01-02_150405.log`）
- **连接安全**：`config.yaml` 中的数据库连接串（含密码）在首次启动时自动加密，无需任何手动操作
- **跨平台确认窗口**：Windows（WinForms + WebBrowser）与 macOS（JXA/Cocoa）都支持 `Allow Keyword`、`Allow Header`、`Execute`、`Cancel`
- **单可执行文件**：独立二进制（需安装 Oracle Instant Client）

<img src="https://www.alvinliu.com/wp-content/uploads/2026/04/db_mcp_whilelist.png" alt="db_mcp confirmation window" />

## 环境要求

### 运行时依赖

1. **Oracle Instant Client**
   - 从 [Oracle 官网](https://www.oracle.com/database/technologies/instant-client/downloads.html) 下载
   - 建议 19c 或更高版本
   - 所需文件：`oci.dll`、`oraociei19.dll`（Windows）或对应 `.dylib`（macOS）

2. **环境配置**
   ```bash
   # Windows：将 Instant Client 加入 PATH
   set PATH=C:\path\to\instantclient;%PATH%
   
   # macOS：设置库路径
   export DYLD_LIBRARY_PATH=/path/to/instantclient:$DYLD_LIBRARY_PATH
   ```

## 安装

### 下载预编译包（推荐）

预编译包发布在 [GitHub Releases](https://github.com/kjstart/cursor_oracle_mcp_server/releases)，无需自行编译。

1. **下载**对应平台的压缩包：
   - **Windows**：`oracle-mcp-server-windows-amd64-<版本>.zip`
   - **macOS Apple Silicon (M1/M2/M3)**：`oracle-mcp-server-darwin-arm64-<版本>.tar.gz`
   - **macOS Intel**：`oracle-mcp-server-darwin-amd64-<版本>.tar.gz`

2. **解压**后得到：
   - 可执行文件（如 `oracle-mcp-server-windows-amd64.exe` 或 `oracle-mcp-server-darwin-arm64`）
   - `user_guide.md` — 分步设置说明
   - `config.yaml.example` — 复制为 `config.yaml` 后编辑

3. **安装 Oracle Instant Client**（见上方 [环境要求](#环境要求)），并加入 `PATH`（Windows）或设置 `ORACLE_HOME`、`DYLD_LIBRARY_PATH`（macOS）。

4. **配置** `config.yaml` 中的数据库连接，再将本服务加入 Cursor MCP（见下方 [配置](#配置) 与 [在 Cursor 中使用](#在-cursor-中使用)）。

完整步骤可参考 [user_guide.md](user_guide.md)。

## 配置

将 `config.yaml.example` 复制为 `config.yaml`，在 `oracle.connections` 下至少配置一个连接：

```yaml
oracle:
  connections:
    database1: "user/pass@//host:1521/ORCL"
    # database2: "user/pass@//host2:1521/ORCL"

security:
  # danger_keywords 匹配方式："whole_text" 或 "tokens"
  danger_keyword_match: "whole_text"

  danger_keywords:
    - truncate
    - drop
    - delete
    - create
    - update
    - execute immediate
    # ... (见 config.yaml.example)

  require_confirm_for_ddl: true   # DDL 始终需确认

logging:
  audit_log: true
  verbose_logging: true
  log_file: "audit.log"
```

数据库工具每次都需要传入已配置的 `connection` 名称，单连接也一样。已知连接名时直接使用；仅在连接名未知或明确需要查看最近诊断时调用 `list_connections`。

> **连接安全：** 首次启动时，`config.yaml` 中的明文连接串会被自动替换为加密值，文件原地更新，注释与格式完整保留。

### v2.0.0：延迟连接与 SSH 隧道

```yaml
oracle:
  lazy_mode: true
  connections:
    prod: 'app/db-password@//db.internal:1521/SERVICE'
    direct: 'app/db-password@//direct-db:1521/SERVICE'
  connection_ssh:
    prod:
      host: bastion.example.com
      username: opc
      private_key_file: '~/.ssh/id_rsa'
      remote_host: db.internal
      remote_port: 1521
logging:
  error_log_file: error.log
```

`host` 是 bastion 的 SSH 地址；`remote_host` 是 bastion 可访问的数据库地址。程序仅登录 bastion，并转发数据库 TCP 端口，不登录数据库服务器。每个连接独立配置，没有 `connection_ssh` 条目的连接继续直连。

`known_hosts_file` 可以省略，默认使用运行用户的 `~/.ssh/known_hosts`（Windows/macOS 通用）。SSH 端口默认 22，数据库目标端口默认 1521。本地端口自动分配。SSH 配置保持明文，数据库 DSN 保留原有加密规则。`password` 可替代私钥；完整认证选项见配置模板。

`lazy_mode: true` 时，启动和 `list_connections` 不联网，所有配置连接返回 `available: true`，表示已配置，不代表已经验证可达。查看 `connection_checked` 和 SSH/数据库状态字段可区分尚未尝试与曾经失败。每次数据库操作前程序先检查、必要时自动重连 SSH 和数据库，再发送 SQL；通常一次调用即可。连接准备失败时可以重试同一个操作，无需先调用 `list_connections`。SQL 已发送后断线不会自动重放；手动重跑写操作前应先确认结果。

关闭 lazy 模式会恢复启动连接、列出连接时重试及 SSH 后台维护；操作前检查在两种模式下都执行。SSH 目前只支持单目标 TCP Easy Connect，TNS 别名、TCPS/wallet、RAC/SCAN 重定向不支持通过此隧道使用。错误诊断写到独立 `error.log`，与 audit log 分离。

### 环境变量

| 变量 | 说明 |
|------|------|
| `ORACLE_MCP_CONFIG` | 配置文件路径（覆盖默认位置） |
| `ORACLE_HOME` | Oracle 客户端安装路径 |
| `PATH`（Windows） | 须包含 Instant Client 目录 |
| `TNS_ADMIN` | **连接 Oracle 自治数据库 (ADB) 时必填** — 存放 `tnsnames.ora` 及钱包文件的目录（如从 ADB 下载的 Wallet zip 解压后的目录） |

### Oracle 自治数据库 (ADB) 与 Wallet

ADB 使用 **TCPS (SSL)**，需要 **Wallet**。连接串使用钱包中 `tnsnames.ora` 的 TNS 别名（如 `mcpdemo_high`）：

1. **下载 Wallet**：Oracle Cloud 控制台 → 你的自治数据库 → **DB 连接** → **下载 Wallet**。解压到某目录（如 `D:\oracle\wallet_mcpdemo`），该目录需包含 `tnsnames.ora`、`sqlnet.ora`、`cwallet.sso`、`ewallet.pem` 等。
2. **config.yaml** — 使用 TNS 别名和数据库用户/密码：
   ```yaml
   oracle:
     connections:
       mcpdemo: "mcpdemo/YourPassword@mcpdemo_high"
   ```
3. **Cursor MCP** — 在 MCP 配置中设置 `TNS_ADMIN` 为 **Wallet 目录**，以便进程找到 `tnsnames.ora` 和 SSL 证书。Windows 下还需在 `PATH` 中包含 Instant Client：
   ```json
   {
     "mcpServers": {
       "oracle": {
         "command": "D:\\path\\to\\oracle-mcp-server-windows-amd64.exe",
         "args": [],
         "env": {
           "TNS_ADMIN": "D:\\oracle\\wallet_mcpdemo",
           "PATH": "C:\\path\\to\\instantclient;%PATH%"
         }
       }
     }
   }
   ```
   将 `D:\oracle\wallet_mcpdemo` 换成你的 Wallet 解压路径，并确保 Instant Client 在 `PATH` 中。未设置 `TNS_ADMIN` 可能出现 **ORA-12541**（无监听）或 SSL 相关错误。

## 在 Cursor 中使用

### MCP 配置

在 Cursor 的 MCP 设置（`~/.cursor/mcp.json` 或工作区 `.cursor/mcp.json`）中添加：

**Windows（Oracle 客户端已在系统 PATH 中）：**
```json
{
  "mcpServers": {
    "oracle": {
      "command": "C:\\path\\to\\oracle-mcp-server-windows-amd64.exe",
      "args": []
    }
  }
}
```

**macOS**

通过 `env` 让 MCP 进程能读到 `ORACLE_HOME` 和 `DYLD_LIBRARY_PATH`：

```json
{
  "mcpServers": {
    "oracle": {
      "command": "/path/to/oracle-mcp-server-darwin-arm64",
      "args": [],
      "env": {
        "ORACLE_HOME": "/opt/oracle/instantclient_19_20",
        "DYLD_LIBRARY_PATH": "/opt/oracle/instantclient_19_20"
      }
    }
  }
}
```

将 `/path/to/oracle-mcp-server-darwin-arm64` 和 `/opt/oracle/instantclient_19_20` 换成你的实际路径。若从已设置环境的终端启动 Cursor，也可用 `"ORACLE_HOME": "${env:ORACLE_HOME}"` 引用现有环境变量。

### 工具说明

| 工具 | 说明 |
|------|------|
| **execute_sql** | 执行 SQL（单条或多条）。参数：`sql`，必填 `connection`。 |
| **execute_sql_file** | 从文件读取 SQL，分析、必要时展示确认，再执行。末尾 `/` 会被去除。参数：`file_path`，必填 `connection`。 |
| **list_connections** | 默认 lazy 模式只列配置，不建立或检查连接；所有已配置连接返回 `available: true`。数据库操作前自动检查并恢复所选连接；关闭 lazy 模式时列表也会重试失败项。 |
| **query_to_csv_file** | 执行查询并将结果写入文件为 CSV（表头+行，UTF-8，RFC 4180）。参数：`sql`、`file_path`（绝对路径），必填 `connection`。与 `execute_sql` 相同：命中危险词或 DDL（若开启）时弹出审查。 |
| **query_to_text_file** | 执行查询并将结果写入文件为纯文本（制表符分隔、无表头；CLOB 完整输出，如存过程源码）。参数：`sql`、`file_path`（绝对路径），必填 `connection`。与 `execute_sql` 相同：命中危险词或 DDL（若开启）时弹出审查。 |

### 使用示例

```
// 单连接：无需传 connection
execute_sql({ "sql": "SELECT table_name FROM user_tables" })

// 多连接
execute_sql({ "sql": "SELECT * FROM my_table", "connection": "database1" })
execute_sql({ "sql": "CREATE TABLE test (id NUMBER)", "connection": "database2" })

// 执行 SQL 文件（末尾 / 会被去除）
execute_sql_file({ "file_path": "d:\\scripts\\myscript.sql", "connection": "ps" })

// 查看连接名称与状态
list_connections()

// 将查询结果写入 CSV 或文本文件（file_path 须为绝对路径；需要时与 execute_sql 相同审查规则）
query_to_csv_file({ "sql": "SELECT * FROM my_table", "file_path": "d:\\out\\data.csv", "connection": "database1" })
query_to_text_file({ "sql": "SELECT text FROM user_source WHERE name='MY_PROC'", "file_path": "d:\\out\\my_proc.sql" })
```

## 安全与确认窗口

当 SQL 命中 `danger_keywords` 或为 DDL（且 `require_confirm_for_ddl` 为 true）时，会弹出确认窗口：

- **关键词展示**：窗口中的 `Keywords` 显示真正命中的 expanded keyword，而不是原始 danger keyword
- **Allow Header**：把当前 SQL 第一行写入 `whitelist.json`；同一连接下第一行再次匹配时直接跳过 review
- **Allow Keyword**：只写入一个 keyword 到 `whitelist.json`，不会批准当前 review；可以在同一个窗口里连续添加多个 keyword
- **Keyword 白名单行为**：某个 keyword 被放行后，只会消掉它自己对应的 expanded keyword 触发项；如果 SQL 里还有其他未放行危险项，仍然会弹出 review
- **白名单位置**：`whitelist.json` 位于程序目录 / 可执行文件目录
- **Windows**：WinForms 窗口，SQL 语法高亮（WebBrowser）
- **macOS**：JXA/Cocoa 窗口，交互语义与 Windows 一致

用户确认后才会执行。拒绝会记录并返回 `USER_REJECTED`。

## SQL 执行规则

- **单条语句**：一条 SQL，可有可无末尾分号。
- **多条语句**：每行一条，**每行以分号结尾**。按顺序执行。
- **PL/SQL**：CREATE PROCEDURE/FUNCTION/PACKAGE（含文件头部 `--` 或 `/* */`）及匿名块（BEGIN...END; / DECLARE...END;）视为一整块，不拆分。
- **从文件**：单独一行的 SQL*Plus `/` 会在执行前移除。

## 审计日志

- **键值格式**：`AUDIT_TIME=...`、`AUDIT_CONNECTION=...`、`AUDIT_KEYWORDS=...`、`AUDIT_APPROVED=...`、`AUDIT_ACTION=...`，以及可选的 `HEADER_LINE=...`、`EXPANDED_KEYWORDS=...`，然后是 `AUDIT_SQL=` 后跟完整 SQL，再以 `######AUDIT_END######` 作为记录分隔。
- **轮转**：单文件 10MB。启动时复用最近未满的日志文件；写满后新建带创建日期的文件，如 `audit_2006-01-02_150405.log`。

## MCP 协议

### 工具：`execute_sql`

**输入**：`sql`（必填），`connection`（必填）。

**输出（查询）**：`columns`、`rows`、`statement_type`、`execution_time_ms`、`success`。

**输出（DML/DDL）**：`rows_affected`、`statement_type`、`execution_time_ms`、`success`，可选 `warning`。

**错误（用户拒绝）**：`code` -32000，`message` "Execution cancelled by user"，`data.code` "USER_REJECTED"，`data.matched_keywords`。

### 工具：`execute_sql_file`

**输入**：`file_path`（必填），`connection`（必填）。与 `execute_sql` 相同的分析与确认规则；执行文件内容（末尾 `/` 去除）。

### 工具：`list_connections`

**输入**：无。**输出**：`lazy_mode`、`connections`、`message`。连接包含 `name`、`available`、`connection_checked`、`ssh_enabled`、`ssh_connected`、`database_connected`，以及可选的 `error_code`、`last_error`、`user_action`。默认 lazy 模式不联网；`available: true` 表示已配置，其余字段反映最近状态。每次数据库操作前检查并恢复所选连接。

### 工具：`query_to_csv_file`

**输入**：`sql`（必填）、`file_path`（必填，绝对路径）、`connection`（必填）。**输出**：成功及路径。与 `execute_sql` 相同：命中 `danger_keywords` 或为 DDL（若 `require_confirm_for_ddl`）时弹出确认。写入带表头的 CSV，UTF-8，RFC 4180；CLOB 列完整读取。

### 工具：`query_to_text_file`

**输入**：`sql`（必填）、`file_path`（必填，绝对路径）、`connection`（必填）。**输出**：成功及路径。与 `execute_sql` 相同：命中 `danger_keywords` 或为 DDL（若 `require_confirm_for_ddl`）时弹出确认。写入纯文本，列以制表符分隔、无表头；CLOB 完整输出（如存过程源码）。

## 故障排除

### 连接问题

```
Error: ORA-12541: TNS:no listener
```
→ 确认 Oracle Instant Client 在 PATH 中且监听正常

```
Error: DPI-1047: Cannot locate a 64-bit Oracle Client library
```
→ 安装 Oracle Instant Client 并加入 PATH

### 权限问题

```
Error: ORA-01031: insufficient privileges
```
→ 当前配置的数据库用户权限不足

## 从源码构建（可选）

若希望自行编译（例如使用不同 Go 版本或平台）：

### 构建依赖

- Go 1.26+
- **须启用 CGO**：godror 依赖 CGO 和 C 编译器。
- **Windows**：使用 **MinGW-w64**（GCC 7.2+）并将其 `bin` 加入 PATH。
  - **不要用 Cygwin 的 gcc。** 若两者都有，确保 PATH 中 MinGW 的 `bin` 在 Cygwin 之前，否则可能出现 `cannot parse gcc output ... as ELF, Mach-O, PE, XCOFF`。
  - 可用 Chocolatey：`choco install mingw`，或 [MSYS2](https://www.msys2.org/) 后 `pacman -S mingw-w64-ucrt-x86_64-gcc`，使用 `mingw64\bin`。
  - 用 `where gcc` 和 `gcc -v` 检查，应看到 "mingw" 或 "MinGW" 及 64 位 (x86_64)。
- **macOS**：执行 `xcode-select --install` 安装命令行工具。

### 构建命令

**注意**：需在启用 CGO 且已安装 GCC 的环境下构建，否则会出现 `undefined: VersionInfo` 等错误。

```bash
# 克隆仓库
git clone https://github.com/kjstart/cursor_oracle_mcp_server
cd cursor_oracle_mcp_server

# 下载依赖
go mod tidy

# Windows (PowerShell)：启用 CGO，确保 gcc 在 PATH 中
$env:CGO_ENABLED="1"
go build -o oracle-mcp.exe .

# Windows (CMD)
set CGO_ENABLED=1
go build -o oracle-mcp.exe .

# macOS (Intel)
CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -o oracle-mcp .

# macOS (Apple Silicon)
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -o oracle-mcp .
```

### 构建故障排除

| 错误 | 原因 | 处理 |
|------|------|------|
| `undefined: VersionInfo` | 未启用 CGO | 设置 `CGO_ENABLED=1` 并安装 gcc |
| `gcc not found` | 未安装 gcc 或不在 PATH | 安装 MinGW-w64 并将其 `bin` 加入 PATH |
| `cannot parse gcc output ... as ELF, Mach-O, PE, XCOFF` | 使用了错误的 gcc（如 **Cygwin gcc**） | 使用 **MinGW-w64** 的 gcc，并保证在 PATH 中位于 Cygwin 之前；用 `where gcc` 和 `gcc -v` 核对 |

修改 gcc 或 PATH 后，清理缓存再构建：

```bash
go clean -cache
go build -o oracle-mcp.exe .
```

## 许可证

MIT License - 见 [LICENSE](LICENSE) 文件

## 参与贡献

1. Fork 本仓库
2. 创建功能分支
3. 提交 Pull Request

## 致谢

- [godror](https://github.com/godror/godror) - Go 版 Oracle 驱动
- [Model Context Protocol](https://modelcontextprotocol.io/) - MCP 规范
