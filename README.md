# ai-usage

A CLI tool that shows how much of your Codex (ChatGPT plan) and Claude usage limits is left (%) and when the limits reset.
For Codex, it also shows when your usage reset tickets expire.

## Install

```bash
go install github.com/atotto/ai-usage@latest
```

## Usage

```bash
ai-usage            # Both Codex and Claude
ai-usage codex      # Codex only (also accepts chatgpt)
ai-usage claude     # Claude only
ai-usage -json      # Output as JSON
```

```
Codex (plus)
  5h               remaining  88.0%  [##################..]  reset in 3h42m (10/17 14:22)
  weekly           remaining  70.0%  [##############......]  reset in 5d05h20m (10/22 16:00)
  reset ticket     Full reset (Weekly + 5 hr)  expires in 5d17h44m (10/23 04:24)
  reset ticket     Full reset (Weekly + 5 hr)  expires in 12d15h54m (10/30 02:34)
  reset ticket     Full reset (Weekly + 5 hr)  expires in 20d20h46m (11/07 07:26)
```

Only unused reset tickets are shown, with the soonest expiry first.

If fetching usage fails for one provider, the tool shows an error and continues.
It exits with code 1 only if fetching usage fails for all selected providers.

## Requirements and notes

| Provider | Login credentials | Data source |
|---|---|---|
| Codex | Codex CLI ChatGPT login in `$CODEX_HOME/auth.json` (default: `~/.codex/auth.json`) | `https://chatgpt.com/backend-api/wham/usage`<br>`https://chatgpt.com/backend-api/wham/rate-limit-reset-credits` |
| Claude | Claude Code login in `~/.claude/.credentials.json` | `https://api.anthropic.com/api/oauth/usage` |

- The Codex limits are for Codex usage included in your ChatGPT plan. They are separate from ChatGPT chat limits.
- For Codex, sign in with your ChatGPT account using `codex login`. API key login is not supported because it has no plan usage limits.
- This tool does not refresh expired Claude tokens. Run `claude` once to refresh your token.
- Both providers use private endpoints that may change without notice.
- This is an unofficial tool and is not affiliated with Anthropic or OpenAI. Use it at your own risk.
- Claude reset tickets are not shown because they cannot be fetched with Claude Code login credentials.

## Main flags

| Flag | Description |
|---|---|
| `-json` | Output as JSON |
| `-timeout` | HTTP timeout (default: 15 seconds) |
| `-codex-auth` | Path to the Codex auth file |
| `-claude-credentials` | Path to the Claude Code credentials file |
