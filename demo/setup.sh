#!/bin/bash
set -euo pipefail

DEMO_DIR="/tmp/fleet-demo"
FLEET="./build/fleet"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$ROOT_DIR"

echo "=== fleet demo setup ==="

# Clean previous demo if exists.
bash "$SCRIPT_DIR/cleanup.sh" 2>/dev/null || true

# Build.
echo "Building..."
make build -s

# --- Create demo repos ---

mkdir -p "$DEMO_DIR"

# Repo 1: Go web API
REPO1="$DEMO_DIR/api-server"
mkdir -p "$REPO1/cmd/server" "$REPO1/internal/handlers"
cd "$REPO1"
git init -q -b main
cat > go.mod <<'EOF'
module github.com/example/api-server

go 1.24
EOF
cat > cmd/server/main.go <<'EOF'
package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok")
	})
	http.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		// TODO: implement
		w.WriteHeader(http.StatusNotImplemented)
	})
	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
EOF
cat > internal/handlers/users.go <<'EOF'
package handlers

type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// TODO: implement CRUD handlers
// TODO: add input validation
// TODO: add authentication middleware
EOF
git add -A && git commit -q -m "init: basic Go web API"

# Fake remote so the main clone and its worktree share ONE origin in fleet's
# sidebar. Origin identity comes from remote.origin.url (git.GetOriginKey);
# without a remote each folder falls back to a distinct local:<basename> and
# the two checkouts split into separate groups instead of grouping together.
git remote add origin https://github.com/example/api-server.git

# Feature work lives in a linked worktree (the real fleet flow), not the main
# checkout — this is what yields the "origin → main repo + worktree" grouping.
# Main stays on `main` (clean); the worktree holds feat/auth (dirty + PR #42).
WT1="$DEMO_DIR/api-server-feat-auth"
git worktree add -q "$WT1" -b feat/auth
echo "// wip" >> "$WT1/internal/handlers/users.go"

# Repo 2: React dashboard
REPO2="$DEMO_DIR/dashboard-ui"
mkdir -p "$REPO2/src/components"
cd "$REPO2"
git init -q -b main
cat > package.json <<'EOF'
{
  "name": "dashboard-ui",
  "version": "0.1.0",
  "dependencies": {
    "react": "^19.0.0",
    "react-dom": "^19.0.0",
    "recharts": "^2.15.0"
  }
}
EOF
cat > src/App.tsx <<'EOF'
import React from 'react';
import { Sidebar } from './components/Sidebar';

export default function App() {
  return (
    <div className="app">
      <Sidebar />
      <main>
        <h1>Dashboard</h1>
        {/* TODO: add analytics charts */}
        {/* TODO: add user activity feed */}
      </main>
    </div>
  );
}
EOF
cat > src/components/Sidebar.tsx <<'EOF'
import React from 'react';

interface NavItem {
  label: string;
  path: string;
  icon: string;
}

const items: NavItem[] = [
  { label: 'Overview', path: '/', icon: 'home' },
  { label: 'Analytics', path: '/analytics', icon: 'chart' },
  { label: 'Users', path: '/users', icon: 'people' },
  { label: 'Settings', path: '/settings', icon: 'gear' },
];

export function Sidebar() {
  return (
    <nav className="sidebar">
      {items.map(item => (
        <a key={item.path} href={item.path}>{item.icon} {item.label}</a>
      ))}
    </nav>
  );
}
EOF
git add -A && git commit -q -m "init: React dashboard scaffold"
git checkout -q -b feat/analytics

# Repo 3: Python ML pipeline
REPO3="$DEMO_DIR/ml-pipeline"
mkdir -p "$REPO3/src" "$REPO3/tests"
cd "$REPO3"
git init -q -b main
cat > requirements.txt <<'EOF'
numpy>=1.26
pandas>=2.1
scikit-learn>=1.4
matplotlib>=3.8
EOF
cat > src/pipeline.py <<'EOF'
"""ML training pipeline."""
import json
from pathlib import Path


def load_data(path: str) -> list:
    """Load training data from JSON file."""
    with open(path) as f:
        return json.load(f)


def preprocess(data: list) -> list:
    """Clean and normalize data."""
    # TODO: handle missing values
    # TODO: feature scaling
    return data


def train(data: list) -> dict:
    """Train the model and return metrics."""
    # TODO: implement model training
    # TODO: cross-validation
    return {"accuracy": 0.0, "f1": 0.0}


def save_model(model: dict, path: str) -> None:
    """Serialize trained model to disk."""
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    with open(path, 'w') as f:
        json.dump(model, f)
EOF
cat > tests/test_pipeline.py <<'EOF'
from src.pipeline import load_data, preprocess, train


def test_preprocess_empty():
    assert preprocess([]) == []


def test_train_returns_metrics():
    result = train([])
    assert "accuracy" in result
    assert "f1" in result
EOF
git add -A && git commit -q -m "init: ML pipeline scaffold"
git checkout -q -b fix/preprocessing

cd "$ROOT_DIR"

# --- Create sessions and send prompts ---

echo ""
echo "Creating sessions..."

# Helper: wait for Claude's input prompt (❯ on a line by itself)
wait_for_claude() {
    local sess="$1"
    local max_wait=45
    local waited=0
    while [ $waited -lt $max_wait ]; do
        # Claude's prompt shows ❯ — match any line containing just ❯ (possibly with spaces)
        if tmux capture-pane -t "$sess" -p 2>/dev/null | grep -q '❯'; then
            sleep 2  # Give Claude a moment to be fully interactive
            return 0
        fi
        sleep 1
        waited=$((waited + 1))
    done
    echo "  Warning: Claude prompt not detected for $sess after ${max_wait}s"
    return 1
}

# Helper: send a prompt to a tmux session (text first, then Enter separately)
send_prompt() {
    local sess="$1"
    local prompt="$2"
    tmux send-keys -t "$sess" "$prompt"
    sleep 0.5
    tmux send-keys -t "$sess" Enter
}

# Step 1: Create all sessions first
SESSIONS=()
PROMPTS=(
    "Review the entire codebase architecture. List every file, explain the structure, and suggest 5 detailed improvements with implementation plans for each"
    "Add JWT authentication middleware and create test files for all handlers"
    "Build a complete analytics dashboard component with charts using recharts, filters, and real-time data updates"
    "What does this project do? Summarize in one sentence."
    "Implement the full training pipeline: data preprocessing with missing value handling, model training with scikit-learn, cross-validation, and evaluation metrics"
)
REPOS=("$REPO1" "$WT1" "$REPO2" "$REPO2" "$REPO3")
LABELS=(
    "api-server: architecture review"
    "api-server (feat/auth worktree): add auth middleware"
    "dashboard-ui: analytics component"
    "dashboard-ui: quick question"
    "ml-pipeline: implement training"
)

BEFORE=$(tmux list-sessions -F '#{session_name}' 2>/dev/null | grep '^fleet_' | sort || true)

for i in "${!REPOS[@]}"; do
    echo "  [$((i+1))/5] Creating ${LABELS[$i]}..."
    $FLEET add "${REPOS[$i]}"
    sleep 1
    NEW_SESS=$(comm -13 <(echo "$BEFORE") <(tmux list-sessions -F '#{session_name}' 2>/dev/null | grep '^fleet_' | sort) | head -1)
    SESSIONS+=("$NEW_SESS")
    BEFORE=$(tmux list-sessions -F '#{session_name}' 2>/dev/null | grep '^fleet_' | sort || true)
done

# Step 2: Wait for all sessions in parallel, then send prompts
echo ""
echo "Waiting for Claude to initialize in all sessions (parallel)..."

# Launch a background job per session that waits + sends prompt
wait_and_send() {
    local idx="$1" sess="$2" label="$3" prompt="$4"
    if [ -z "$sess" ]; then
        echo "  [!] Session $((idx+1)) not found, skipping"
        return
    fi
    if wait_for_claude "$sess"; then
        echo "  [$((idx+1))/5] Sending: ${label}"
        send_prompt "$sess" "$prompt"
    fi
}

for i in "${!SESSIONS[@]}"; do
    wait_and_send "$i" "${SESSIONS[$i]}" "${LABELS[$i]}" "${PROMPTS[$i]}" &
done
wait

echo ""
echo "=== Demo setup complete ==="
echo ""
echo "Sessions are starting. Wait ~30s for them to reach various states, then run:"
echo ""
echo "  FLEET_DEMO_PREFIX=/tmp/fleet-demo PATH=\"$SCRIPT_DIR:\$PATH\" ./build/fleet"
echo ""
echo "To clean up later: bash demo/cleanup.sh"
