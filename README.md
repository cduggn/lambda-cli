# lam — Lambda Cloud from the terminal

A single-file bash CLI over the [Lambda Cloud REST API](https://docs-api.lambda.ai/api/cloud) that turns
"click through the console, wait, ssh in, install stuff" into one command:

```bash
lam launch -c vllm --name lab        # 1x A100 40GB SXM4, us-east-1, Lambda Stack 22.04, key inference-eng
# … launched, waits for boot + sshd + cloud-init, then prints:
ssh -i ~/.ssh/inference-eng.pem ubuntu@129.x.x.x
lam ssh                              # drop in;  ~/venv already has vLLM
lam rm                               # terminate when done
```

## How Lambda lets you skip the setup (the "Chef" question)

Lambda has **no custom images / snapshots** on on-demand instances. What it has instead:

| Mechanism | What it gives you |
|---|---|
| **Image families** (`lambda-stack-22-04`, `lambda-stack-24-04`, `gpu-base-*`, `ubuntu-*`) | Lambda Stack ships CUDA, cuDNN, NCCL, driver, PyTorch, JAX, container toolkit, JupyterLab. Nothing to install for basic GPU work. |
| **`user_data` = cloud-init** on the launch API call | Your bootstrap script runs at first boot as root, before you even ssh in. This is the Chef/Ansible replacement for a throwaway box. Max 1 MB, plain text. |
| **REST API** (`POST /instance-operations/launch`) | Everything the console does. Bearer token auth. Launch is rate-limited to one call per 12 s. |
| **Persistent filesystems** | Network volume you attach at launch for model weights / venvs that must survive termination. Optional; `lam` doesn't attach one unless you add it. |

`lam` = API + cloud-init templates + the boring waiting.

## Install

```bash
git clone https://github.com/cduggn/lambda-cli ~/workspace/lambda-cli
~/workspace/lambda-cli/install.sh        # symlinks ~/.local/bin/lam, creates ~/.config/lam/config
$EDITOR ~/.config/lam/config             # set LAMBDA_API_KEY (https://cloud.lambda.ai/api-keys)
lam config                               # sanity check
lam keys                                 # confirm the ssh key name you use exists (inference-eng)
```

Needs `curl`, `jq`, `ssh`, `perl` (all present on macOS). Works on macOS's bash 3.2.

## Defaults

Set in `~/.config/lam/config` or as env vars; all overridable per call.

| Var | Default |
|---|---|
| `LAM_TYPE` | `gpu_1x_a100_sxm4` (1x A100 40 GB SXM4, ~$1.29/hr) |
| `LAM_REGION` | `us-east-1` |
| `LAM_SSH_KEY` | `inference-eng` (name registered in Lambda) |
| `LAM_SSH_PRIVATE_KEY` | `~/.ssh/inference-eng.pem` |
| `LAM_IMAGE_FAMILY` | `lambda-stack-22-04` |
| `LAM_CLOUD_INIT` | none (`base`, `vllm`, or a path) |

No filesystem, no extra firewall ruleset are ever sent; the account's global ruleset applies (port 22 must be open there).

## Commands

```
lam launch [opts]           launch, wait for ssh + cloud-init, print the ssh command
lam ls                      instances + $/hr burn rate
lam ssh [ID|NAME] [-- CMD]  ssh in (no arg = the one running instance)
lam env [ID]                print export LAMBDA=…/LAMBDA_SSH_KEY=… (matches the class .env format)
lam wait [ID]               block until ssh + cloud-init are done
lam logs [ID]               tail /var/log/cloud-init-output.log
lam rm [ID…|--all] [-y]     terminate
lam types [-r REGION] [-a]  instance types, prices, regions with capacity
lam images [REGION]         image families / ids
lam keys | keys add NAME FILE.pub
lam render NAME|FILE        show a template with {{VARS}} filled in
lam config [init]
```

`lam launch --help` lists the launch flags. Useful ones:

```bash
lam launch --dry-run                          # show the exact JSON that would be POSTed
lam launch --retry 30m                        # keep retrying on insufficient-capacity (every 15 s)
lam launch -t gpu_1x_h100_pcie -r us-west-1   # different box
lam launch --no-wait                          # just print the instance id
HF_TOKEN=hf_… MODEL=Qwen/Qwen3-0.6B lam launch -c vllm   # template vars come from your shell
```

## cloud-init templates (`cloud-init/`)

`{{VAR}}` placeholders are filled from your environment at launch time (unset = empty + warning).

- `base.yaml`: tmux, htop, nvtop, rsync, `uv`. Marker `~/.lam-ready`.
- `vllm.yaml`: base + `~/venv` with `vllm>=0.28` (own pinned torch), `~/.hf_env` with `HF_TOKEN`, optional `MODEL` prefetch
  via `snapshot_download`, both sourced from `~/.bashrc`. Writes `~/.lam-vllm-check` with the import/CUDA result.

Add your own: drop `cloud-init/<name>.yaml` in and `lam launch -c <name>`. Debug a run with `lam logs`; cloud-init
logs live at `/var/log/cloud-init-output.log` on the instance.

Timing expectation: boot to ssh is a couple of minutes; the vLLM install adds roughly 3 to 5 more (large wheels).
Since it starts at boot, it overlaps with the time you would have spent watching the console.

## Using it with the class labs

`lam env` prints exactly the two variables `setup/sync_to_lambda.sh` and `setup/ssh.sh` expect:

```bash
lam launch -c vllm --name class7
lam env >> ~/workspace/inference-eng/class-code/class7/.env
cd ~/workspace/inference-eng/class-code/class7 && bash setup/sync_to_lambda.sh && bash setup/ssh.sh
```

## The raw API call (no tool)

```bash
curl -s https://cloud.lambda.ai/api/v1/instance-operations/launch \
  -H "Authorization: Bearer $LAMBDA_API_KEY" -H 'Content-Type: application/json' \
  -d "$(jq -n --rawfile ud cloud-init/vllm.yaml '{
        region_name: "us-east-1", instance_type_name: "gpu_1x_a100_sxm4",
        ssh_key_names: ["inference-eng"], image: {family: "lambda-stack-22-04"},
        name: "lab", user_data: $ud }')"
# → {"data":{"instance_ids":["…"]}}   then poll GET /instances/{id} until status=active, ip set
```

## Alternatives considered

- Community Terraform providers ([elct9620/lambdalabs](https://registry.terraform.io/providers/elct9620/lambdalabs/latest),
  [squat/terraform-provider-lambda](https://github.com/squat/terraform-provider-lambda)). Fine for long-lived infra;
  overkill for a box you spin up for an hour.
- SkyPilot / dstack support Lambda as a backend and add job scheduling and multi-cloud failover. Worth it when
  "find me any A100 anywhere" matters more than a fixed region.
- Unofficial Python CLI [`lamblbs`](https://pypi.org/project/lamblbs/). No cloud-init or wait-for-ssh.

## Notes

- Lambda requires exactly one ssh key name per launch.
- Terminated instances still show in `lam ls` briefly with status `terminated`.
- Rate limits: 1 request/s generally, 1 launch per 12 s.
