# Telemetry

oku has optional, **opt-in** usage telemetry. It is off by default and nothing is ever sent unless you turn it on yourself. It helps us see which commands, chains and routes people use and where they fail (for example a swap stuck on `REJECTED` for every route), so we can fix problems we would otherwise never hear about.

## Turn it on or off

```sh
oku telemetry status    # on or off, why, the install ID, and every field that is sent
oku telemetry enable    # opt in (stored in the global config file, not per profile)
oku telemetry disable   # opt out and delete the install ID
```

Precedence, strongest first:

1. `DO_NOT_TRACK=1` (see <https://consoledonottrack.com>) always forces telemetry off, even if enabled in config.
2. `OKU_TELEMETRY=0` or `OKU_TELEMETRY=1` overrides the config for a single run.
3. `oku telemetry enable` / `disable` in the global config.
4. Default: off.

Agents and MCP clients must never enable telemetry for the user. Only a human runs `oku telemetry enable`.

After a successful interactive `oku auth login` oku prints a single stderr line, once per install, saying telemetry exists and is off. It is never shown in `--json` mode, without a terminal, or when `DO_NOT_TRACK` is set.

## What is sent

At most one event, `cli_command`, per command run, posted at exit to the Oku PostHog project through the first-party proxy `https://piaget.icarus.tools`. The `distinct_id` is a random install UUID stored in the state directory (`telemetry_id`). It is created only when telemetry is enabled and deleted by `oku telemetry disable`. It is never derived from a wallet, account, Privy ID, session, email, hostname or username.

Properties (an exhaustive allowlist enforced in code; anything else is dropped before sending):

| Field | Meaning |
| --- | --- |
| `$lib`, `source` | always `oku-cli` |
| `$process_person_profile` | always `false` |
| `command` | command path, e.g. `swap prepare` |
| `success` | true or false |
| `error_code` | structured error code such as `REJECTED`, `INSUFFICIENT_GAS`, `OUTCOME_UNKNOWN` (failures only) |
| `retryable` | the error's retryable flag (failures only) |
| `duration_ms` | command wall time |
| `cli_version`, `cli_commit` | build info |
| `os`, `arch` | e.g. `linux`, `amd64` |
| `json` | whether `--json` was used |
| `invoked_by` | `mcp`, `telegram`, `tty` or `non_tty` |
| `api_env` | `prod`, `staging`, `local` or `custom` (the URL itself is never sent) |
| `chain_id`, `destination_chain` | funds commands: numeric chain IDs |
| `route` | funds commands: the route or bridge the CLI selected on a successful quote, prepare, submit or status, otherwise the `--route` value |
| `route_forced` | funds commands: `true` if `--route` was passed, `false` if the flag exists but was not used |
| `intent` | funds commands: whether intent routes were allowed |
| `amount_source` | funds commands: `max` or `exact` (never the amount) |
| `from_kind`, `to_kind` | funds commands: `native` or `erc20` (never the token address) |
| `routes_considered`, `routes_rejected`, `route_attempts` | funds `REJECTED` failures: number of routes considered and rejected, and quote attempts |
| `route_rejections` | funds `REJECTED` failures: counts per rejection category such as `amount_mismatch:2,not_answered:1,simulation_failed:1`. Only the category before the first `:` is sent, unknown categories become `other`; route names, backend messages and amounts are never sent |

**Never sent:** wallet, agent, account or token addresses, amounts, transaction hashes, intent IDs, digests, order IDs, quote contents, error message text, credentials or tokens, file paths, CSV contents, environment variables, hostnames or usernames.

## Guarantees

- Telemetry never writes to stdout, never changes a command's output or exit code, and never runs in the background after the command finishes. The single send is bounded to 2 seconds at exit and every error is ignored. Nothing is queued or retried.
- `oku mcp serve` sends one event when the server exits and never writes anything except protocol frames to stdout.
- For testing, `OKU_TELEMETRY_HOST` and `OKU_TELEMETRY_KEY` override the ingestion host and project key.
