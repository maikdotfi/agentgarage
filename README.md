# Agent Garage

This is an experiment of what happens when we let agents shape the software that builds the agents. What? Yeah, exactly. I have no idea.

## The bucket

Both sides reach the garage only through an R2 bucket. Put its credentials in
`r2.env` next to the keys (`~/.garage/r2.env` on a laptop, `/etc/garage/r2.env`
on the host), or in the same variables in the environment:

```sh
GARAGE_R2_ENDPOINT=https://<account>.r2.cloudflarestorage.com
GARAGE_R2_BUCKET=garage
GARAGE_R2_ACCESS_KEY_ID=...
GARAGE_R2_SECRET_ACCESS_KEY=...
```

What `garage serve` runs is `config/garage.json` in the bucket, written from
the laptop:

```sh
garage remote config <<'EOF'
{
  "git_name": "<bot>", "git_email": "<bot email>",
  "workspaces": {"agentgarage": "https://github.com/maikdotfi/agentgarage"}
}
EOF
```

`model` (default `claude-sonnet-5`) and `model_url` are optional.

`serve` also reads the model from its environment, which wins over the config:
`GARAGE_MODEL`, `GARAGE_MODEL_URL` (any Anthropic-compatible endpoint, such as
`https://ollama.com`) and `GARAGE_MODEL_KEY`, the name of the secret holding
the key (default `ANTHROPIC_API_KEY`). `GARAGE_DEV_*` and `GARAGE_GRUG_*`
set one agent's. On the host they go in `/etc/garage/serve.env`:

```sh
GARAGE_MODEL_URL=https://ollama.com
GARAGE_MODEL_KEY=OLLAMA_API_KEY
GARAGE_MODEL=<a model Ollama Cloud serves>
GARAGE_GRUG_MODEL=<another, just for grug>
```

## On a laptop (Milestone A)

One laptop plays both sides, so one `~/.garage` holds the signing key, the
master key and its recipient. `serve` execs `git`, `gh` and
[`mise`](https://mise.jdx.dev), which installs each workspace's toolchain from
its `mise.toml`.

```sh
go build -o garage ./cmd/garage
./garage init -master

# Secrets go to the bucket encrypted; the value is read from stdin.
read -rs v && printf %s "$v" | ./garage remote secret GH_TOKEN
read -rs v && printf %s "$v" | ./garage remote secret ANTHROPIC_API_KEY
./garage remote config < garage.json

./garage serve

# in another terminal: one room per task
./garage chat -room hello
@dev add a hello section to the README in agentgarage
```

## On the VPS (Milestone B)

A Debian host, reached as root over SSH with a key. The only things copied
over are the binary and what it can't make itself.

```sh
./garage init                       # on the laptop, once: prints its public key
GOOS=linux GOARCH=amd64 go build -o garage-linux ./cmd/garage

ssh root@host mkdir -p /etc/garage
scp r2.env root@host:/etc/garage/
# rebuilding a dead host only: the offline copies of both host keys
# scp master.key signing.key root@host:/etc/garage/
scp garage-linux root@host:garage
ssh root@host ./garage setup -ssh-from <your ip or LAN range> -trust <laptop public key>
```

`setup` prints the host's public key and the master recipient. Add the first
to the laptop's `~/.garage/trusted.keys` and write the second to
`~/.garage/recipient`, then write the secrets and config as above. `serve`
retries every 30s until they're there. On first setup, keep offline copies of
`/etc/garage/master.key` and `/etc/garage/signing.key`: a rebuild needs both.

Then talk to it through the bucket, or over SSH:

```sh
./garage remote chat -room hello    # mail; replies show up within ~10s
ssh root@host garage chat -room hello
```

## Kicking the tires

This section was added by the dev agent from the garage chat UI, running on its own host.

Deploying by hand means copying a new binary and running `garage setup`
again: it restarts `serve` only if something changed. Snapshots run daily
(`garage backup`), and a host with no databases restores the latest ones when
`serve` starts. So a rebuild is the same copies and the same command.
