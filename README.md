# Kompadre

## Install

```shell
brew tap SamuAlfageme/tap
brew install --cask kompadre
```

## Usage

```shell
# Interactive TUI: choose kubeconfigs, then compare commands
kompadre

# Start the TUI with both kubeconfigs selected
kompadre ~/.kube/staging ~/.kube/prod

# Run a unified prompt on launch
kompadre ~/.kube/staging ~/.kube/prod "kubectl get pods -A"

# Run a unified prompt and open directly on the delta view
kompadre --delta ~/.kube/staging ~/.kube/prod "kubectl get pods -A"

# Headless mode: print the delta diff to stdout, no TUI
kompadre --print ~/.kube/staging ~/.kube/prod "kubectl get pods -A"

# Pipe-friendly output
kompadre --print ~/.kube/staging ~/.kube/prod "kubectl get nodes -o yaml" | less -R
```
