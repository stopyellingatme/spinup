#!/usr/bin/env python3
"""Measure one cold target per candidate. Do not reuse target directories."""
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import time

ROOT = Path(__file__).resolve().parent

def uptime():
    text = subprocess.check_output(['uptime'], text=True).strip()
    match = re.search(r'load averages?:\s*([0-9.]+)', text)
    return {'uptime': text, 'load1': float(match[1]) if match else None}

if os.environ.get('R0_CC_LOG') and len(__import__('sys').argv) > 1:
    # Cargo invokes this file as a CC wrapper. Record compile calls, not probes.
    import sys
    args = sys.argv[1:]
    state = uptime()
    start = time.perf_counter()
    result = subprocess.run(['/usr/bin/cc', *args], check=False)
    if '-c' in args and any(a.endswith('.c') for a in args):
        item = dict(state, seconds=time.perf_counter()-start, arguments=args,
                    exit_code=result.returncode)
        with open(os.environ['R0_CC_LOG'], 'a') as log:
            log.write(json.dumps(item)+'\n')
    sys.exit(result.returncode)

records = []
for feature in ['path', 'edit', 'splice']:
    # The shared target parent is authorized. Each run gets a new clean target.
    target = Path(os.environ['CARGO_TARGET_DIR']).parent / f'r01-cost-{feature}-{time.time_ns()}'
    target.mkdir()
    log = target / 'cc.jsonl'
    env = dict(os.environ, CARGO_TARGET_DIR=str(target), RUSTC_WRAPPER='',
               CC=str(Path(__file__).resolve()), R0_CC_LOG=str(log))
    command = ['cargo', 'build', '--no-default-features', '--features', feature,
               '--offline', '--locked']
    state = uptime()
    start = time.perf_counter()
    with open(target / 'build.log', 'w') as output:
        result = subprocess.run(command, cwd=ROOT, env=env, stdout=output,
                                stderr=subprocess.STDOUT, check=False)
    duration = time.perf_counter()-start
    compiles = [json.loads(s) for s in log.read_text().splitlines()] if log.exists() else []
    records.append(dict(feature=feature, command=command, seconds=duration,
                        before=state, after=uptime(), exit_code=result.returncode,
                        c_compiles=compiles))
    print(feature, round(duration, 3), 'seconds', state, 'exit', result.returncode, flush=True)
    if result.returncode:
        print((target / 'build.log').read_text())
        break
report = {'machine': platform.platform(), 'compiler': subprocess.check_output(['/usr/bin/cc', '--version'], text=True).splitlines()[0],
          'cargo_build_jobs': os.environ.get('CARGO_BUILD_JOBS'), 'rustc_wrapper': '',
          'records': records}
(ROOT / 'build-cost.json').write_text(json.dumps(report, indent=2)+'\n')
raise SystemExit(any(r['exit_code'] for r in records))
