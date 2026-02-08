# crash

Tests database durability by spawning writer subprocesses and killing them with SIGKILL at random intervals.

## Usage

```bash
# Run crash test with 50 cycles (default)
./bin/crash --dbdir=/tmp/crashtest

# Customize cycle count and kill timing
./bin/crash --dbdir=/tmp/crashtest --cycles=100 --min-delay=5 --max-delay=50

# Run writer subprocess manually (for debugging)
./bin/crash --mode=writer --dbdir=/tmp/testdb --state=/tmp/state.txt
```

Example output:
```
2026/02/08 15:03:32 Starting crash orchestrator: 30 cycles
2026/02/08 15:03:32 Cycle 0: spawning writer subprocess
2026/02/08 15:03:32 Cycle 0: killing subprocess with SIGKILL after 47ms
...
2026/02/08 15:03:47 Results: 70 recovered, 10 lost out of 80 total
```
