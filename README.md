# Agent Garage

This is an experiment of what happens when we let agents shape the software that builds the agents. What? Yeah, exactly. I have no idea.

## Running it on a laptop (Milestone A)

One laptop plays both sides, so one `GARAGE_HOME` (default `~/.garage`) holds
the signing key, the master key and its recipient.

```sh
go build -o garage ./cmd/garage
./garage init -master

export GARAGE_R2_ENDPOINT=https://<account>.r2.cloudflarestorage.com
export GARAGE_R2_BUCKET=garage
export GARAGE_R2_ACCESS_KEY_ID=... GARAGE_R2_SECRET_ACCESS_KEY=...

# Secrets go to the bucket encrypted; the value is read from stdin.
read -rs v && printf %s "$v" | ./garage remote secret GH_TOKEN
read -rs v && printf %s "$v" | ./garage remote secret ANTHROPIC_API_KEY

./garage serve -workspace agentgarage=https://github.com/maikdotfi/agentgarage \
  -git-name <bot> -git-email <bot email>

# in another terminal: one room per task
./garage chat -room hello
@dev add a hello section to the README in agentgarage
```
