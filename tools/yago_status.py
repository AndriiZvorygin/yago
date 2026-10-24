#!/usr/bin/env python3

import argparse
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from http.cookiejar import CookieJar
from pathlib import Path


DEFAULT_CONTAINER = "yago-yacy-migration-full"
DEFAULT_OPS_PORT = 9090
HTTP_TIMEOUT_SECONDS = 4


def docker_inspect(container):
    result = subprocess.run(
        ["docker", "inspect", container],
        check=True,
        capture_output=True,
        text=True,
    )
    records = json.loads(result.stdout)
    if len(records) != 1:
        raise ValueError("expected one inspected container")
    return records[0]


def environment_map(inspect):
    result = {}
    for entry in inspect.get("Config", {}).get("Env", []):
        name, separator, value = entry.partition("=")
        if separator:
            result[name] = value
    return result


def ipv6_operations_urls(inspect, port):
    urls = []
    networks = inspect.get("NetworkSettings", {}).get("Networks", {})
    for network in networks.values():
        address = network.get("GlobalIPv6Address", "")
        if address and address != "invalid IP":
            urls.append(f"http://[{address}]:{port}")
    return urls


def safe_readiness(payload):
    checks = []
    for check in payload.get("checks", []):
        item = {
            "name": check.get("name", "unknown"),
            "ready": bool(check.get("ready")),
        }
        for key in ("backend", "documents", "updatedAt", "reason"):
            if key in check:
                item[key] = check[key]
        checks.append(item)
    return {
        "httpStatus": payload.get("_http_status"),
        "ready": bool(payload.get("ready")),
        "generatedAt": payload.get("generatedAt"),
        "checks": checks,
    }


def safe_backfill_status(payload):
    fields = (
        "state",
        "scannedDocuments",
        "indexedDocuments",
        "skippedDocuments",
        "postingRows",
        "estimatedBytes",
        "maximumDocumentsRun",
        "dryRun",
        "updatedAt",
    )
    result = {key: payload[key] for key in fields if key in payload}
    result["lastErrorPresent"] = bool(payload.get("lastError"))
    return result


def fetch_json(opener, url, method="GET", body=None, timeout=HTTP_TIMEOUT_SECONDS):
    data = None if body is None else json.dumps(body).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=data,
        method=method,
        headers={"Accept": "application/json", "Content-Type": "application/json"},
    )
    try:
        with opener.open(request, timeout=timeout) as response:
            body = response.read()
            payload = {} if not body else json.loads(body)
            payload["_http_status"] = response.status
            return response.status, payload, ""
    except urllib.error.HTTPError as error:
        return error.code, {}, "http_error"
    except TimeoutError:
        return None, {}, "timeout"
    except urllib.error.URLError as error:
        if isinstance(error.reason, (TimeoutError, socket.timeout)):
            return None, {}, "timeout"
        return None, {}, "network_error"
    except json.JSONDecodeError:
        return None, {}, "invalid_json"
    except OSError:
        return None, {}, "network_error"


def operations_status(inspect, environment):
    address = environment.get("YAGO_OPS_ADDR", f":{DEFAULT_OPS_PORT}")
    match = re.search(r":([0-9]{1,5})$", address)
    port = int(match.group(1)) if match else DEFAULT_OPS_PORT
    for base_url in ipv6_operations_urls(inspect, port):
        status, payload, error = fetch_json(
            urllib.request.build_opener(), base_url + "/ready"
        )
        if status is not None:
            payload["_http_status"] = status
            return base_url, safe_readiness(payload), error
    return "", {"httpStatus": None, "ready": False, "checks": []}, "unreachable"


def admin_backfill_status(base_url, environment):
    username = environment.get("YAGO_ADMIN_USER", "")
    password = environment.get("YAGO_ADMIN_PASSWORD", "")
    if not username or not password:
        return {"available": False, "reason": "credentials_not_configured"}

    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))
    status, _, error = fetch_json(
        opener,
        base_url + "/api/admin/v1/auth/login",
        method="POST",
        body={"username": username, "password": password},
        timeout=15,
    )
    if status != 200:
        return {
            "available": False,
            "reason": f"login_{status}" if status is not None else error,
        }

    status, payload, error = fetch_json(opener, base_url + "/admin/index/rwi-backfill")
    try:
        fetch_json(opener, base_url + "/api/admin/v1/auth/logout", method="POST", body={})
    except Exception:
        pass
    if status != 200:
        return {
            "available": False,
            "reason": f"status_{status}" if status is not None else error,
        }
    return {"available": True, **safe_backfill_status(payload)}


def public_status(environment):
    base = environment.get("YAGO_PUBLIC_BASE_URL", "").rstrip("/")
    if not base:
        return {"available": False, "reason": "public_url_not_configured"}
    request = urllib.request.Request(base + "/", method="GET")
    try:
        with urllib.request.urlopen(request, timeout=HTTP_TIMEOUT_SECONDS) as response:
            return {"available": True, "httpStatus": response.status}
    except urllib.error.HTTPError as error:
        return {"available": True, "httpStatus": error.code}
    except (TimeoutError, urllib.error.URLError, OSError):
        return {"available": False, "reason": "unreachable"}


def docker_stats(container):
    result = subprocess.run(
        ["docker", "stats", "--no-stream", "--format", "{{json .}}", container],
        check=True,
        capture_output=True,
        text=True,
    )
    record = json.loads(result.stdout)
    return {
        "cpu": record.get("CPUPerc"),
        "memory": record.get("MemUsage"),
        "memoryPercent": record.get("MemPerc"),
        "networkIO": record.get("NetIO"),
        "blockIO": record.get("BlockIO"),
        "pids": record.get("PIDs"),
    }


def mount_status(inspect):
    result = []
    for mount in inspect.get("Mounts", []):
        source = mount.get("Source", "")
        item = {
            "source": source,
            "destination": mount.get("Destination", ""),
            "type": mount.get("Type", ""),
        }
        if source:
            candidate = Path(source)
            while True:
                try:
                    usage = shutil.disk_usage(candidate)
                    item["capacityPath"] = str(candidate)
                    item["freeBytes"] = usage.free
                    item["totalBytes"] = usage.total
                    break
                except OSError as error:
                    if candidate.parent == candidate:
                        item["capacityError"] = type(error).__name__
                        break
                    candidate = candidate.parent
        result.append(item)
    return result


def recent_log_counts(container):
    patterns = {
        "vault_errors": re.compile(r"vault.*(error|failed|timeout)|(?:error|failed|timeout).*vault", re.I),
        "split_events": re.compile(r"split", re.I),
        "deadline_errors": re.compile(r"deadline exceeded|context deadline", re.I),
        "shard_contention": re.compile(r"shard contended|storage contended", re.I),
        "resource_guard_events": re.compile(r"resource guard|headroom", re.I),
    }
    counts = {name: 0 for name in patterns}
    process = subprocess.Popen(
        ["docker", "logs", "--since", "1h", container],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    if process.stdout is not None:
        for line in process.stdout:
            for name, pattern in patterns.items():
                counts[name] += bool(pattern.search(line))
        process.stdout.close()
    return_code = process.wait()
    if return_code != 0:
        raise subprocess.CalledProcessError(return_code, process.args)
    return counts


def git_status(root):
    revision = subprocess.run(
        ["git", "-C", str(root), "rev-parse", "--short", "HEAD"],
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
    branch = subprocess.run(
        ["git", "-C", str(root), "branch", "--show-current"],
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
    dirty = bool(
        subprocess.run(
            ["git", "-C", str(root), "status", "--porcelain"],
            check=True,
            capture_output=True,
            text=True,
        ).stdout
    )
    return {"branch": branch, "revision": revision, "dirty": dirty}


def collect(container, root):
    inspect = docker_inspect(container)
    environment = environment_map(inspect)
    base_url, readiness, readiness_error = operations_status(inspect, environment)
    return {
        "generatedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "container": {
            "name": inspect.get("Name", "").lstrip("/"),
            "state": inspect.get("State", {}).get("Status"),
            "restarts": inspect.get("RestartCount"),
            "startedAt": inspect.get("State", {}).get("StartedAt"),
            "image": inspect.get("Config", {}).get("Image"),
            "imageID": inspect.get("Image"),
            "sourceRevision": inspect.get("Config", {}).get("Labels", {}).get(
                "org.opencontainers.image.revision", "unknown"
            ),
        },
        "source": git_status(root),
        "runtime": docker_stats(container),
        "mounts": mount_status(inspect),
        "readiness": readiness,
        "readinessError": readiness_error,
        "adminOperationsURL": base_url,
        "rwiBackfill": admin_backfill_status(base_url, environment) if base_url else {
            "available": False,
            "reason": "operations_listener_unreachable",
        },
        "public": public_status(environment),
        "recentLogCounts": recent_log_counts(container),
    }


def render_report(report):
    container = report["container"]
    readiness = report["readiness"]
    lines = [
        f"Container: {container['name']} ({container['state']}, restarts={container['restarts']})",
        f"Image: {container['image']} ({container['sourceRevision']})",
        f"Source: {report['source']['branch']} {report['source']['revision']} dirty={report['source']['dirty']}",
        f"Runtime: CPU {report['runtime']['cpu']}, memory {report['runtime']['memory']} ({report['runtime']['memoryPercent']}), PIDs {report['runtime']['pids']}",
        f"Readiness: ready={readiness['ready']} HTTP={readiness['httpStatus']}",
    ]
    for check in readiness.get("checks", []):
        lines.append(
            "Index: "
            f"{check.get('backend', 'unknown')}, docs={check.get('documents', 'unknown')}, "
            f"updated={check.get('updatedAt', 'unknown')}"
        )
    backfill = report["rwiBackfill"]
    if backfill.get("available"):
        lines.append(
            f"RWI backfill: {backfill.get('state')}, scanned={backfill.get('scannedDocuments')}, "
            f"indexed={backfill.get('indexedDocuments')}, postings={backfill.get('postingRows')}, "
            f"updated={backfill.get('updatedAt')}"
        )
    else:
        lines.append(f"RWI backfill: unavailable ({backfill.get('reason', 'unknown')})")
    lines.append(f"Public site: available={report['public']['available']} HTTP={report['public'].get('httpStatus', 'n/a')}")
    for mount in report["mounts"]:
        free = mount.get("freeBytes")
        free_text = "unknown" if free is None else f"{free / (1024 ** 3):.1f} GiB free"
        lines.append(f"Mount: {mount['source']} -> {mount['destination']} ({free_text})")
    log_counts = report["recentLogCounts"]
    lines.append(
        "Log counts (1h): "
        + ", ".join(f"{name}={value}" for name, value in log_counts.items())
    )
    return "\n".join(lines)


def main(argv=None):
    parser = argparse.ArgumentParser(description="Inspect the local Yago node without printing secrets.")
    parser.add_argument("--container", default=os.environ.get("YAGO_STATUS_CONTAINER", DEFAULT_CONTAINER))
    parser.add_argument("--json", action="store_true", help="emit redacted JSON")
    arguments = parser.parse_args(argv)
    root = Path(__file__).resolve().parent.parent
    try:
        report = collect(arguments.container, root)
    except (OSError, subprocess.CalledProcessError, json.JSONDecodeError, ValueError) as error:
        print(f"Yago status failed: {type(error).__name__}", file=sys.stderr)
        return 1
    print(json.dumps(report, indent=2, sort_keys=True) if arguments.json else render_report(report))
    return 0 if report["readiness"]["ready"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
