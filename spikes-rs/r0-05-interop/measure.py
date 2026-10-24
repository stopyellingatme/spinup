#!/usr/bin/env python3
"""Record a check duration beside the machine load from uptime."""
import datetime
import json
import os
from pathlib import Path
import subprocess
import sys
import time

name, *command = sys.argv[1:]
log = Path(__file__).resolve().with_name("measurements.json")
before = subprocess.check_output(["uptime"], text=True).strip()
load_before = os.getloadavg()[0]
started = datetime.datetime.now(datetime.timezone.utc).isoformat()
start = time.monotonic()
result = subprocess.run(command, check=False)
elapsed = time.monotonic() - start
load_after = os.getloadavg()[0]
after = subprocess.check_output(["uptime"], text=True).strip()
records = json.loads(log.read_text()) if log.exists() else []
records.append({"name": name, "command": command, "cwd": os.getcwd(),
                "started_utc": started, "elapsed_s": round(elapsed, 3),
                "exit_code": result.returncode, "load1_before": load_before,
                "load1_after": load_after, "uptime_before": before,
                "uptime_after": after})
log.write_text(json.dumps(records, indent=2) + "\n")
print(f"{name}: {elapsed:.3f} s. uptime before: {before}. uptime after: {after}", flush=True)
sys.exit(result.returncode)
