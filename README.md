<div align="center">

# lam

**Lambda Cloud GPU instances from the command line**

Launch a GPU, bootstrap it, sync your code, watch the spend, tear it down. No console, no clicking.

[![CI](https://github.com/cduggn/lambda-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/cduggn/lambda-cli/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)](https://go.dev)

</div>

```console
$ lam launch -c vllm --name lab
launch: gpu_1x_a100_sxm4 in us-east-1  image=lambda-stack-22-04  key=inference-eng  cloud-init=embedded:vllm  $1.29/hr
launched instance 0920582c7ff041399e34823a0be62549
  status=booting ip=- (10s)
active: ubuntu@129.213.0.0  (booted in 1m40s)
ssh ready (22s)
waiting for cloud-init (embedded:vllm) … tail with: lam logs 0920582c7ff041399e34823a0be62549
cloud-init done

ready in 6m12s.   ssh: lam ssh    env: eval "$(lam env)"    kill: lam rm 0920582c7ff041399e34823a0be62549
```

Your venv already has vLLM. Your model is already pulled. You never opened a browser.

## Why this exists

Spinning up a GPU for an hour of testing should not cost you ten minutes of setup.
The console makes you click through a wizard, then you SSH in and install the same
packages you installed yesterday.

Lambda has **no custom images and no snapshots** on on-demand instances, so the
"just bake an AMI" answer is off the table. What it does have is enough:

| Mechanism | What it gives you |
|---|---|
| **Image families** `lambda-stack-22-04`, `gpu-base-*`, `ubuntu-*` | Lambda Stack already ships the NVIDIA driver, CUDA, cuDNN, NCCL, PyTorch, JAX and JupyterLab. Nothing to install for basic GPU work. |
| **`user_data`, which is cloud-init** | Your bootstrap runs at first boot as root, before you can even SSH in. This is the Chef and Ansible replacement for a throwaway box. Plain text, 1 MB cap. |
| **The REST API** | Everything the console does. Bearer token auth. One launch call per 12 seconds. |
| **Persistent filesystems** | A network volume for weights or venvs that must outlive the instance. Optional. `lam` never attaches one unless you ask. |

`lam` is an API client, a set of cloud-init templates, and all the waiting done for you.
The bootstrap runs *while the machine boots*, so it is mostly free wall-clock time.

## Features

- **One command to a working box** - launch, wait for boot, wait for sshd, wait for cloud-init, print the SSH line
- **Bootstrap templates** - embedded cloud-init for a bare box or a full vLLM venv, with `{{VAR}}` filled from your shell
- **Capacity retry** - `--retry 30m` keeps asking when your GPU is sold out, instead of failing at you
- **Spend visibility** - `lam ls -u` shows uptime and estimated cost per instance and in total
- **File sync** - `lam push` and `lam pull` wrap rsync with the address and key already filled in
- **Safe by default** - no filesystem attached, no firewall changes, and terminate always confirms

## Quick start

```bash
git clone https://github.com/cduggn/lambda-cli ~/workspace/lambda-cli
cd ~/workspace/lambda-cli && make install

lam config init                     # writes ~/.config/lam/config
$EDITOR ~/.config/lam/config        # set LAMBDA_API_KEY
lam keys                            # first live call: confirms the key works

lam launch --name test              # a plain Lambda Stack box
lam ssh                             # get on it
lam rm                              # stop paying for it
```

> [!TIP]
> Start with `lam launch --dry-run`. It prints the exact JSON it would POST and
> costs nothing.

## Installation

<details>
<summary><strong>From source</strong> (the supported path today)</summary>

```bash
git clone https://github.com/cduggn/lambda-cli ~/workspace/lambda-cli
cd ~/workspace/lambda-cli
make install          # -> $(go env GOPATH)/bin/lam
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`. To update: `git pull && make install`.

Needs `ssh` and, for file sync, `rsync`. No other runtime dependencies.

</details>

<details>
<summary><strong>Homebrew and go install</strong> (not yet)</summary>

Both need the repository to be public with a tagged release. Until then they fail:
Homebrew cannot fetch release assets and `go install` cannot resolve the module
through the public proxy.

See [Release](#release) for what turns them on.

</details>

## Usage

### Launching

```bash
lam launch                                    # config defaults
lam launch -c vllm --name lab                 # with the vLLM bootstrap
lam launch -t gpu_1x_h100_pcie -r us-west-1   # a different box
lam launch --retry 30m                        # wait out a capacity shortage
lam launch --dry-run                          # show the request, send nothing
lam launch --no-wait                          # print the id and return

HF_TOKEN=hf_… MODEL=Qwen/Qwen3-0.6B lam launch -c vllm   # template vars from your shell
```

Ctrl-C during a wait cancels cleanly. The instance keeps running, and `lam wait`
picks the waiting back up.

### What is running, and what it costs

```bash
lam ls        # instances, hourly rate, total burn rate
lam ls -u     # adds uptime and estimated spend
```

```console
$ lam ls -u
ID                                NAME  TYPE              REGION     STATUS  IP           $/HR  UPTIME  SPENT
0920582c7ff041399e34823a0be62549  lab   gpu_1x_a100_sxm4  us-east-1  active  129.213.0.0  1.29  3h25m   4.41

burn rate: $1.29/hr across running instances
spent so far: $4.41 (estimate from the activity log, not a bill)
```

> [!NOTE]
> Lambda's instance objects carry no launch timestamp and the API has no billing
> endpoint. `-u` derives uptime from the account activity log, taking the earliest
> event naming an instance as its launch. That is one extra API call, it also
> covers instances you started from the console, and the figure is an estimate
> rather than an invoice.

### Moving files

```bash
lam push                       # cwd -> ~/<dirname> on the instance
lam push ./class7 '~/class7/'  # explicit source and destination
lam pull '~/class7/results' .  # bring results back
lam push -n                    # dry run
lam push -- --delete           # anything after -- goes straight to rsync
```

A thin wrapper: it fills in the address and your key, then hands off to the real
`rsync`. Trailing slashes keep their usual meaning, so `src/` copies the contents
of a directory and `src` copies the directory itself. The command is echoed before
it runs.

### Terminating

```bash
lam rm            # the one running instance, with a confirmation
lam rm lab -y     # by name, no prompt
lam rm -a -y      # everything
```

## Bootstrap templates

Templates are embedded in the binary and rendered at launch. `{{VAR}}` placeholders
come from your environment; anything unset renders empty and warns.

| Template | What it does |
|---|---|
| `base` | tmux, htop, nvtop, rsync, `uv`. Marker at `~/.lam-ready`. |
| `vllm` | `base` plus `~/venv` with `vllm>=0.28`, `HF_TOKEN` wired into `~/.hf_env`, optional `MODEL` prefetch, both sourced from `~/.bashrc`. Result of the import and CUDA check lands in `~/.lam-vllm-check`. |

```bash
lam render vllm                 # see exactly what would run, vars filled in
lam logs                        # tail cloud-init on the box
```

Add your own as `~/.config/lam/cloud-init/NAME.yaml`, which also overrides an
embedded name, or pass any file path to `-c`. Boot to SSH is a couple of minutes;
the vLLM install adds roughly three to five more, overlapping the boot you were
waiting on anyway.

## Configuration

Precedence is environment, then `~/.config/lam/config`, then built-in defaults.
Everything is overridable per command.

| Setting | Default | Notes |
|---|---|---|
| `LAMBDA_API_KEY` | none | Required. From [cloud.lambda.ai/api-keys](https://cloud.lambda.ai/api-keys). |
| `LAM_TYPE` | `gpu_1x_a100_sxm4` | `lam types` lists the rest with prices. |
| `LAM_REGION` | `us-east-1` | |
| `LAM_SSH_KEY` | `inference-eng` | The key **name** registered with Lambda, from `lam keys`. |
| `LAM_SSH_PRIVATE_KEY` | `~/.ssh/inference-eng.pem` | The matching private key on this machine. |
| `LAM_IMAGE_FAMILY` | `lambda-stack-22-04` | `lam images` lists them. |
| `LAM_CLOUD_INIT` | none | Default template for every launch. |
| `LAM_EXCLUDE` | `.venv,__pycache__,.pytest_cache,.git,.env,.DS_Store` | rsync excludes for push and pull. |

`lam config` prints what is actually in effect, which is the fastest way to debug
a surprise.

## Command reference

```
lam launch [flags]          launch, wait for ssh and cloud-init, print the ssh command
lam ls [-u]                 instances, burn rate, optionally uptime and spend
lam ssh [ID|NAME] [-- CMD]  ssh in (no argument means the only running instance)
lam push [SRC] [DEST]       rsync a local directory up
lam pull SRC [DEST]         rsync a remote path down
lam env [ID]                print export LAMBDA=… lines
lam wait [ID]               block until active, ssh up, cloud-init finished
lam logs [ID]               tail /var/log/cloud-init-output.log
lam rm [ID…] [-a] [-y]      terminate
lam types [-r REGION] [-a]  instance types, prices, where capacity exists
lam images [REGION]         image families and ids
lam keys | keys add NAME FILE.pub
lam render NAME|FILE        print a template with vars filled in
lam config [init]           show effective configuration
```

## Development

```bash
make build      # build to bin/
make test       # go test -race ./...
make lint       # go vet + gofmt check
make snapshot   # goreleaser dry run
```

```
cmd/lam/             main
internal/cli/        cobra commands
internal/lambda/     API client, wait and retry helpers
internal/config/     defaults < file < environment
internal/cloudinit/  template lookup, rendering, embedded templates
contrib/             the original bash prototype
```

The API client is covered by `httptest` tests, including capacity retry,
activity-log pagination and the boot poll, so most behaviour is verifiable
without spending money on a GPU.

## Release

Not released yet, because the repository is private.

To turn on Homebrew and `go install`: make the repository public, add the
`PUBLISHER_TOKEN` secret with repo scope on `homebrew-cduggn`, then tag `vX.Y.Z`
and push the tag. GoReleaser builds darwin and linux binaries and pushes the
formula to the tap.

## Notes and limits

- Lambda accepts exactly one SSH key name per launch.
- Rate limits are one request per second, and one launch per 12 seconds.
- Terminated instances linger briefly in `lam ls` with status `terminated`.
- macOS ships openrsync, which reports itself as rsync 2.6.9 compatible and has no
  `--filter`. Gitignore-aware syncing needs a newer rsync from Homebrew.
- macOS has an unrelated `/usr/bin/lam` that laminates text files. Installing this
  shadows it on your `PATH`.

## Alternatives

- **Terraform providers** ([elct9620/lambdalabs](https://registry.terraform.io/providers/elct9620/lambdalabs/latest), [squat/terraform-provider-lambda](https://github.com/squat/terraform-provider-lambda)) suit long-lived infrastructure. Heavy for a box you kill in an hour.
- **SkyPilot** and **dstack** treat Lambda as one backend and add scheduling and multi-cloud failover. Worth it when "any A100 anywhere" beats a fixed region.
- **[`lamblbs`](https://pypi.org/project/lamblbs/)**, an unofficial Python CLI. No cloud-init and no wait-for-ssh.
