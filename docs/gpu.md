# GPU Passthrough

Hiro does not need a GPU. Most users run Hiro against a hosted LLM provider
(Anthropic, OpenAI, OpenRouter, etc.), which does inference on the provider's
hardware. Only read this page if you are running a **local** inference
backend (Ollama, llama.cpp, vLLM, etc.) inside the Hiro container, or
alongside it and want it to use the GPU.

Passing a GPU into a Docker container is vendor-specific. Pick the section
for your hardware and add the snippet to the `hiro` service in
`docker-compose.yml`.

## NVIDIA

Install the [NVIDIA Container
Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)
on the host, then configure the Docker runtime:

```bash
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
```

Add to the service:

```yaml
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
```

Verify with `docker compose exec hiro nvidia-smi`.

## AMD (ROCm)

Install the [ROCm
stack](https://rocm.docs.amd.com/projects/install-on-linux/en/latest/) on the
host. No Docker runtime plugin is required — you pass the device nodes
directly and join the `video`/`render` groups.

```yaml
    devices:
      - /dev/kfd
      - /dev/dri
    group_add:
      - video
      - render
```

Some ROCm workloads additionally need `security_opt: - seccomp:unconfined`.
Hiro's agent worker processes install their own seccomp filter, but the
control plane process relies on the container-level filter — disabling it
weakens the control plane's defenses. Only add this if your workload
actually requires it, and treat it as a tradeoff.

## Intel (iGPU and Arc)

Intel GPUs work via the standard DRI interface. No toolkit or special
runtime is needed:

```yaml
    devices:
      - /dev/dri
    group_add:
      - render
```

For compute workloads (oneAPI, IPEX-LLM), you may also want to pass the
specific render node (e.g. `/dev/dri/renderD128`) and ensure the host has
the [Intel compute
runtime](https://github.com/intel/compute-runtime) installed.

## Group IDs across distros

`video` and `render` GIDs vary by distro. If the container user can't
access the GPU after adding `group_add`, find the host GID and pass it
numerically:

```bash
getent group render
```

Then use the numeric GID in `group_add` (e.g. `- "993"`).
