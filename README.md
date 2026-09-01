# lam — Lambda Cloud from the terminal

A Go CLI over the [Lambda Cloud REST API](https://docs-api.lambda.ai/api/cloud) that turns
"click through the console, wait, ssh in, install stuff" into one command:

```bash
lam launch -c vllm --name lab   # 1x A100 40GB SXM4, us-east-1, Lambda Stack 22.04, key inference-eng
# … launched, waits for boot + sshd + cloud-init, then prints:
ssh -i ~/.ssh/inference-eng.pem ubuntu@129.x.x.x
lam ssh                         # drop in; ~/venv already has vLLM
lam rm                          # terminate when done
```

## How Lambda lets you skip the setup

Lambda has **no custom images or snapshots** on on-demand instances. What it has instead:

| Mechanism | What it gives you |
|---|---|
| **Image families** (`lambda-stack-22-04`, `lambda-stack-24-04`, `gpu-base-*`, `ubuntu-*`) | Lambda Stack ships the NVIDIA driver, CUDA, cuDNN, NCCL, PyTorch, JAX, container toolkit, JupyterLab. |
| **`user_data` = cloud-init** on the launch call | Your bootstrap runs at first boot as root, before you ssh in. This is the Chef/Ansible replacement for a throwaway box. Plain text, max 1 MB. |
| **REST API** `POST /instance-operations/launch` | Everything the console does. Bearer token auth. One launch call per 12 s. |
| **Persistent filesystems** | Network volume attached at launch for weights or venvs that must outlive the instance. Optional; `lam` does not attach one. |

`lam` = API client + embedded cloud-init templates + the waiting.

## Install

From a clone. This is the supported path while the repo is private:

```bash
git clone https://github.com/cduggn/lambda-cli ~/workspace/lambda-cli
cd ~/workspace/lambda-cli && make install     # -> $(go env GOPATH)/bin/lam

lam config init                               # writes ~/.config/lam/config
$EDITOR ~/.config/lam/config                  # set LAMBDA_API_KEY (https://cloud.lambda.ai/api-keys)
lam config && lam keys                        # sanity check; confirm your key name exists
```

Make sure `$(go env GOPATH)/bin` is on your PATH. To pull updates: `git pull && make install`.

Needs `ssh` on the PATH. No other runtime dependencies.

`brew install cduggn/cduggn/lam` and `go install github.com/cduggn/lambda-cli/cmd/lam@latest` both
need the repo to be public with a tagged release. Neither works yet. See [Release](#release).

## Defaults

Precedence: environment > `~/.config/lam/config` > built-in defaults. All overridable per call.

| Var | Default |
|---|---|
| `LAM_TYPE` | `gpu_1x_a100_sxm4` (1x A100 40 GB SXM4, about $1.29/hr) |
| `LAM_REGION` | `us-east-1` |
| `LAM_SSH_KEY` | `inference-eng` (name registered in Lambda) |
| `LAM_SSH_PRIVATE_KEY` | `~/.ssh/inference-eng.pem` |
| `LAM_IMAGE_FAMILY` | `lambda-stack-22-04` |
| `LAM_CLOUD_INIT` | none (`base`, `vllm`, or a path) |
| `LAM_TEMPLATES_DIR` | `~/.config/lam/cloud-init` (your own `NAME.yaml` templates) |

No filesystem and no extra firewall ruleset are ever sent; the account's global ruleset applies, so port 22 must be open there.

## Commands

```
lam launch [flags]          launch, wait for ssh + cloud-init, print the ssh command
lam ls                      instances + $/hr burn rate
lam ssh [ID|NAME] [-- CMD]  ssh in (no arg = the one running instance)
lam env [ID]                print export LAMBDA=… / LAMBDA_SSH_KEY=… (matches the class .env format)
lam wait [ID]               block until active + ssh + cloud-init are done
lam logs [ID]               tail /var/log/cloud-init-output.log
lam rm [ID…] [-a] [-y]      terminate
lam types [-r REGION] [-a]  instance types, prices, regions with capacity
lam images [REGION]         image families / ids
lam keys | keys add NAME FILE.pub
lam render NAME|FILE        show a template with {{VARS}} filled in
lam config [init]
```

Useful launch flags:

```bash
lam launch --dry-run                          # show the exact JSON that would be POSTed
lam launch --retry 30m                        # keep retrying on insufficient-capacity every 15 s
lam launch -t gpu_1x_h100_pcie -r us-west-1   # a different box
lam launch --no-wait                          # just print the instance id
HF_TOKEN=hf_… MODEL=Qwen/Qwen3-0.6B lam launch -c vllm   # template vars come from your shell
```

Ctrl-C during a wait cancels cleanly; the instance keeps running (`lam ls`, `lam wait`).

## cloud-init templates

Embedded in the binary (`internal/cloudinit/templates/`). `{{VAR}}` placeholders are filled from your
environment at launch time; unset vars render empty with a warning.

- `base`: tmux, htop, nvtop, rsync, `uv`. Marker `~/.lam-ready`.
- `vllm`: base + `~/venv` with `vllm>=0.28` (its own pinned torch), `~/.hf_env` with `HF_TOKEN`,
  optional `MODEL` prefetch via `snapshot_download`, both sourced from `~/.bashrc`.
  Writes `~/.lam-vllm-check` with the import and CUDA result.

Your own: drop `NAME.yaml` into `~/.config/lam/cloud-init/` (overrides an embedded name) or pass a path.
Debug with `lam logs`; the full log is `/var/log/cloud-init-output.log` on the instance.

Timing: boot to ssh is a couple of minutes; the vLLM install adds roughly 3 to 5 more. Since it starts
at boot, it overlaps with the time you would have spent watching the console.

## With the class labs

`lam env` prints exactly what `setup/sync_to_lambda.sh` and `setup/ssh.sh` read:

```bash
lam launch -c vllm --name class7
lam env >> ~/workspace/inference-eng/class-code/class7/.env
cd ~/workspace/inference-eng/class-code/class7 && bash setup/sync_to_lambda.sh && bash setup/ssh.sh
```

## Layout

```
cmd/lam/             main
internal/cli/        cobra commands
internal/lambda/     API client, wait/retry helpers (httptest-covered)
internal/config/     defaults < file < env
internal/cloudinit/  template lookup + rendering, embedded templates
contrib/             the original bash prototype (lam.sh) and its installer
```

## Release

Not released yet. The repo is private, so Homebrew cannot fetch release assets and
`go install` cannot resolve the module. Use the clone path above.

When it is ready: make the repo public, add the `PUBLISHER_TOKEN` secret (the same one ccExplorer
uses, needs repo scope on `homebrew-cduggn`), then tag `vX.Y.Z` and push the tag. GoReleaser builds
darwin and linux binaries and pushes the `lam` formula to `cduggn/homebrew-cduggn`.

## Alternatives considered

- Community Terraform providers ([elct9620/lambdalabs](https://registry.terraform.io/providers/elct9620/lambdalabs/latest),
  [squat/terraform-provider-lambda](https://github.com/squat/terraform-provider-lambda)). Good for long-lived infra; heavy for a one-hour box.
- SkyPilot and dstack use Lambda as a backend and add scheduling and multi-cloud failover. Worth it when "any A100 anywhere" matters more than a fixed region.
- Unofficial Python CLI [`lamblbs`](https://pypi.org/project/lamblbs/). No cloud-init or wait-for-ssh.

## Notes

- Lambda requires exactly one ssh key name per launch.
- Terminated instances linger in `lam ls` briefly with status `terminated`.
- Rate limits: 1 request/s generally, 1 launch per 12 s.
