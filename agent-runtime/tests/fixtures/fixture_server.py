#!/usr/bin/env python3
import collections
import gzip
import hashlib
import http.server
import ipaddress
import json
import os
import socket
import socketserver
import struct
import threading
import time
import urllib.parse
import zlib


HTTP_HOST = os.environ.get("FIXTURE_HTTP_HOST", "0.0.0.0")
HTTP_PORT = int(os.environ.get("FIXTURE_HTTP_PORT", "8080"))
DNS_HOST = os.environ.get("FIXTURE_DNS_HOST", "0.0.0.0")
DNS_PORT = int(os.environ.get("FIXTURE_DNS_PORT", "5353"))
STREAM_CHUNK = b"x" * 4096
STREAM_DELAY_SECONDS = 0.01


def bounded_env_int(name, default, minimum, maximum):
    value = int(os.environ.get(name, str(default)))
    if value < minimum or value > maximum:
        raise ValueError(f"{name} must be between {minimum} and {maximum}")
    return value


MAX_REQUEST_BODY = bounded_env_int(
    "FIXTURE_MAX_REQUEST_BODY_BYTES", 8 * 1024 * 1024, 1, 8 * 1024 * 1024
)
MAX_EVENTS = bounded_env_int("FIXTURE_MAX_EVENTS", 256, 1, 1024)
STREAM_MAX_BYTES = bounded_env_int(
    "FIXTURE_STREAM_MAX_BYTES", 64 * 1024 * 1024, len(STREAM_CHUNK), 64 * 1024 * 1024
)
BYTES_MAX_BYTES = bounded_env_int(
    "FIXTURE_BYTES_MAX_BYTES", 8 * 1024 * 1024, len(STREAM_CHUNK), 8 * 1024 * 1024
)


def sha256(data):
    return hashlib.sha256(data).hexdigest()


class State:
    def __init__(self):
        self.lock = threading.Lock()
        self.reset()

    def reset(self):
        with getattr(self, "lock", threading.Lock()):
            self.requests_total = 0
            self.responses_total = 0
            self.request_bytes = 0
            self.response_bytes = 0
            self.disconnects = 0
            self.server_errors = 0
            self.next_request_id = 1
            self.events = collections.deque(maxlen=MAX_EVENTS)
            self.route_counts = collections.Counter()
            self.dns_counts = collections.Counter()

    def request(self, event):
        with self.lock:
            request_id = self.next_request_id
            self.next_request_id += 1
            event["request_id"] = request_id
            event["response_status"] = None
            event["response_body_bytes"] = 0
            event["response_body_sha256"] = sha256(b"")
            event["cancellation_reason"] = None
            self.requests_total += 1
            self.request_bytes += event["request_body_bytes"]
            self.route_counts[event["route"]] += 1
            self.events.append(event)
            return request_id

    def response(self, request_id, status, size, digest, disconnected=False):
        with self.lock:
            self.responses_total += 1
            self.response_bytes += size
            if disconnected:
                self.disconnects += 1
            for event in reversed(self.events):
                if event["request_id"] == request_id:
                    event["response_status"] = status
                    event["response_body_bytes"] = size
                    event["response_body_sha256"] = digest
                    event["cancellation_reason"] = (
                        "client_disconnect" if disconnected else None
                    )
                    break

    def server_error(self):
        with self.lock:
            self.server_errors += 1

    def dns(self, name, qtype):
        key = f"{name}:{qtype}"
        with self.lock:
            if key not in self.dns_counts and len(self.dns_counts) >= MAX_EVENTS:
                key = f"<overflow>:{qtype}"
            self.dns_counts[key] += 1
            return self.dns_counts[key]

    def snapshot(self):
        with self.lock:
            return {
                "requests_total": self.requests_total,
                "responses_total": self.responses_total,
                "request_bytes": self.request_bytes,
                "response_bytes": self.response_bytes,
                "disconnects": self.disconnects,
                "server_errors": self.server_errors,
                "limits": {
                    "max_request_body_bytes": MAX_REQUEST_BODY,
                    "max_events": MAX_EVENTS,
                    "stream_max_bytes": STREAM_MAX_BYTES,
                    "bytes_max_bytes": BYTES_MAX_BYTES,
                },
                "route_counts": dict(sorted(self.route_counts.items())),
                "dns_counts": dict(sorted(self.dns_counts.items())),
                "events": list(self.events),
            }


STATE = State()


def route_name(path):
    if path.startswith("/status/"):
        return "/status/:code"
    if path.startswith("/compressed/"):
        return "/compressed/:encoding"
    if path.startswith("/redirect/"):
        return "/redirect/:target"
    if path.startswith("/bytes/"):
        return "/bytes/:n"
    if path in {
        "/items",
        "/upload",
        "/raw",
        "/slow",
        "/endless",
        "/rebound",
    }:
        return path
    return "/other"


def safe_query(raw_query):
    encoded = raw_query.encode("utf-8", "surrogatepass")
    return {"byte_len": len(encoded), "sha256": sha256(encoded)}


def safe_headers(headers):
    result = []
    for name, value in headers.items():
        encoded = value.encode("utf-8", "surrogatepass")
        result.append(
            {
                "name": name.lower()[:128],
                "value_bytes": len(encoded),
                "value_sha256": sha256(encoded),
            }
        )
    return result[:128]


class BoundedHTTPServer(http.server.ThreadingHTTPServer):
    daemon_threads = True
    block_on_close = False

    def __init__(self, address, handler):
        super().__init__(address, handler)
        self.worker_slots = threading.BoundedSemaphore(16)

    def process_request(self, request, client_address):
        self.worker_slots.acquire()
        try:
            super().process_request(request, client_address)
        except BaseException:
            self.worker_slots.release()
            raise

    def process_request_thread(self, request, client_address):
        try:
            super().process_request_thread(request, client_address)
        finally:
            self.worker_slots.release()

    def handle_error(self, _request, _client_address):
        STATE.server_error()


class FixtureHandler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "agent-runtime-fixture/1"
    sys_version = ""

    def log_message(self, _format, *_args):
        return

    def __getattr__(self, name):
        if name.startswith("do_"):
            return self._handle
        raise AttributeError(name)

    do_GET = lambda self: self._handle()
    do_HEAD = lambda self: self._handle()
    do_POST = lambda self: self._handle()
    do_PUT = lambda self: self._handle()
    do_PATCH = lambda self: self._handle()
    do_DELETE = lambda self: self._handle()
    do_OPTIONS = lambda self: self._handle()

    def _read_exact(self, length):
        body = self.rfile.read(length)
        return body if len(body) == length else None

    def _read_chunked_body(self):
        body = bytearray()
        while True:
            line = self.rfile.readline(129)
            if not line.endswith(b"\r\n") or len(line) > 128:
                self._send(400, b"invalid chunk framing\n", "text/plain")
                return None
            raw_size = line[:-2].split(b";", 1)[0]
            try:
                size = int(raw_size, 16)
            except ValueError:
                self._send(400, b"invalid chunk size\n", "text/plain")
                return None
            if size < 0 or size > MAX_REQUEST_BODY - len(body):
                self._send(413, b"request too large\n", "text/plain")
                return None
            if size == 0:
                trailer_bytes = 0
                while True:
                    trailer = self.rfile.readline(1025)
                    trailer_bytes += len(trailer)
                    if trailer == b"\r\n":
                        return bytes(body)
                    if (
                        not trailer.endswith(b"\r\n")
                        or len(trailer) > 1024
                        or trailer_bytes > 8192
                    ):
                        self._send(400, b"invalid chunk trailer\n", "text/plain")
                        return None
            chunk = self._read_exact(size)
            terminator = self._read_exact(2)
            if chunk is None or terminator != b"\r\n":
                self._send(400, b"truncated chunk\n", "text/plain")
                return None
            body.extend(chunk)

    def _read_body(self):
        raw_length = self.headers.get("Content-Length")
        transfer_encoding = self.headers.get("Transfer-Encoding")
        if transfer_encoding is not None:
            if raw_length is not None or transfer_encoding.strip().lower() != "chunked":
                self._send(400, b"invalid transfer encoding\n", "text/plain")
                return None
            return self._read_chunked_body()
        raw_length = raw_length or "0"
        try:
            length = int(raw_length)
        except ValueError:
            self._send(400, b"invalid content length\n", "text/plain")
            return None
        if length < 0 or length > MAX_REQUEST_BODY:
            self._send(413, b"request too large\n", "text/plain")
            return None
        return self._read_exact(length)

    def _record(self, parsed, body):
        encoded_method = self.command.encode("ascii", "backslashreplace")
        encoded_path = parsed.path.encode("utf-8", "surrogatepass")
        event = {
            "method": self.command[:32],
            "method_bytes": len(encoded_method),
            "method_sha256": sha256(encoded_method),
            "path": parsed.path[:512],
            "path_bytes": len(encoded_path),
            "path_sha256": sha256(encoded_path),
            "route": route_name(parsed.path),
            "query": safe_query(parsed.query),
            "headers": safe_headers(self.headers),
            "request_body_bytes": len(body),
            "request_body_sha256": sha256(body),
        }
        return STATE.request(event)

    def _send(self, status, body, content_type, headers=(), request_id=None):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        for name, value in headers:
            self.send_header(name, value)
        self.end_headers()
        sent = 0
        disconnected = False
        if self.command != "HEAD":
            try:
                written = self.wfile.write(body)
                self.wfile.flush()
                sent = len(body) if written is None else written
            except (BrokenPipeError, ConnectionResetError, TimeoutError):
                disconnected = True
        if request_id is not None:
            STATE.response(
                request_id,
                status,
                sent,
                sha256(body[:sent]),
                disconnected,
            )

    def _stream(self, request_id):
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.send_header("Connection", "close")
        self.end_headers()
        sent = 0
        digest = hashlib.sha256()
        disconnected = False
        try:
            chunks = STREAM_MAX_BYTES // len(STREAM_CHUNK)
            for _ in range(chunks):
                self.wfile.write(f"{len(STREAM_CHUNK):x}\r\n".encode("ascii"))
                self.wfile.write(STREAM_CHUNK)
                self.wfile.write(b"\r\n")
                self.wfile.flush()
                sent += len(STREAM_CHUNK)
                digest.update(STREAM_CHUNK)
                time.sleep(STREAM_DELAY_SECONDS)
            self.wfile.write(b"0\r\n\r\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError, TimeoutError):
            disconnected = True
        STATE.response(request_id, 200, sent, digest.hexdigest(), disconnected)

    def _stream_bytes(self, request_id, size):
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(size))
        self.send_header("Connection", "close")
        self.end_headers()
        sent = 0
        digest = hashlib.sha256()
        disconnected = False
        try:
            while sent < size and self.command != "HEAD":
                chunk = b"b" * min(len(STREAM_CHUNK), size - sent)
                written = self.wfile.write(chunk)
                self.wfile.flush()
                written = len(chunk) if written is None else written
                if written <= 0:
                    raise BrokenPipeError
                sent += written
                digest.update(chunk[:written])
                time.sleep(STREAM_DELAY_SECONDS)
        except (BrokenPipeError, ConnectionResetError, TimeoutError):
            disconnected = True
        STATE.response(request_id, 200, sent, digest.hexdigest(), disconnected)

    def _handle(self):
        parsed = urllib.parse.urlsplit(self.path)
        if parsed.path == "/__state" and self.command == "GET":
            body = json.dumps(STATE.snapshot(), sort_keys=True, separators=(",", ":")).encode()
            self._send(200, body, "application/json")
            return
        if parsed.path == "/__reset" and self.command == "POST":
            STATE.reset()
            self._send(204, b"", "application/json")
            return

        body = self._read_body()
        if body is None:
            return
        request_id = self._record(parsed, body)

        if parsed.path == "/items" and self.command == "GET":
            self._send(200, b'{"items":["alpha","beta"]}\n', "application/json", (("X-Fixture", "items"),), request_id)
        elif parsed.path == "/items":
            try:
                parsed_body = json.loads(body) if body else None
                response = json.dumps(parsed_body, sort_keys=True, separators=(",", ":")).encode() + b"\n"
            except (UnicodeDecodeError, json.JSONDecodeError):
                response = b'{"ok":true}\n'
            self._send(200, response, "application/json", request_id=request_id)
        elif parsed.path == "/upload":
            response = json.dumps(
                {"received_bytes": len(body), "sha256": sha256(body)},
                sort_keys=True,
                separators=(",", ":"),
            ).encode() + b"\n"
            self._send(200, response, "application/json", request_id=request_id)
        elif parsed.path == "/raw":
            self._send(200, body, "application/octet-stream", request_id=request_id)
        elif parsed.path == "/slow":
            time.sleep(6)
            self._send(200, b"slow\n", "text/plain", request_id=request_id)
        elif parsed.path == "/endless":
            self._stream(request_id)
        elif parsed.path.startswith("/bytes/"):
            try:
                size = int(parsed.path.rsplit("/", 1)[1])
                if size < 0 or size > BYTES_MAX_BYTES:
                    raise ValueError
            except ValueError:
                self._send(400, b"invalid byte count\n", "text/plain", request_id=request_id)
            else:
                self._stream_bytes(request_id, size)
        elif parsed.path == "/redirect/control":
            self._send(302, b"", "text/plain", (("Location", "http://127.0.0.1:8080/items"),), request_id)
        elif parsed.path == "/redirect/rebind":
            self._send(302, b"", "text/plain", (("Location", "http://rebind.agent.test:8080/rebound"),), request_id)
        elif parsed.path == "/rebound":
            self._send(200, b"rebinding-policy-failed\n", "text/plain", request_id=request_id)
        elif parsed.path.startswith("/status/"):
            try:
                status = int(parsed.path.rsplit("/", 1)[1])
                if status < 100 or status > 599:
                    raise ValueError
            except ValueError:
                status = 400
            self._send(status, f"status={status}\n".encode(), "text/plain", request_id=request_id)
        elif parsed.path == "/compressed/gzip":
            payload = gzip.compress(b"compressed fixture response\n", mtime=0)
            self._send(200, payload, "text/plain", (("Content-Encoding", "gzip"),), request_id)
        elif parsed.path == "/compressed/deflate":
            payload = zlib.compress(b"compressed fixture response\n")
            self._send(200, payload, "text/plain", (("Content-Encoding", "deflate"),), request_id)
        else:
            self._send(404, b"not found\n", "text/plain", request_id=request_id)


def decode_dns_name(packet, offset):
    labels = []
    while True:
        if offset >= len(packet):
            raise ValueError("truncated DNS name")
        length = packet[offset]
        offset += 1
        if length == 0:
            break
        if length & 0xC0:
            raise ValueError("compressed DNS question is unsupported")
        if length > 63 or offset + length > len(packet):
            raise ValueError("invalid DNS label")
        labels.append(packet[offset : offset + length].decode("ascii").lower())
        offset += length
    return ".".join(labels) + ".", offset


def encode_dns_name(name):
    result = bytearray()
    for label in name.rstrip(".").split("."):
        encoded = label.encode("ascii")
        result.append(len(encoded))
        result.extend(encoded)
    result.append(0)
    return bytes(result)


def dns_rr(name, rr_type, payload):
    return encode_dns_name(name) + struct.pack("!HHIH", rr_type, 1, 0, len(payload)) + payload


def dns_response(packet):
    if len(packet) < 12:
        return b""
    transaction_id, flags, questions, _, _, _ = struct.unpack("!HHHHHH", packet[:12])
    if questions != 1 or flags & 0x8000:
        return struct.pack("!HHHHHH", transaction_id, 0x8181, 0, 0, 0, 0)
    try:
        name, offset = decode_dns_name(packet, 12)
        if offset + 4 > len(packet):
            raise ValueError("truncated DNS question")
        qtype, qclass = struct.unpack("!HH", packet[offset : offset + 4])
    except (UnicodeDecodeError, ValueError):
        return struct.pack("!HHHHHH", transaction_id, 0x8181, 0, 0, 0, 0)
    question = packet[12 : offset + 4]
    count = STATE.dns(name, qtype)
    answers = []
    if qclass == 1 and qtype == 1:
        if name == "fixture.agent.test.":
            answers.append(dns_rr(name, 1, ipaddress.IPv4Address("11.0.0.10").packed))
        elif name == "mixed.agent.test.":
            answers.append(dns_rr(name, 1, ipaddress.IPv4Address("11.0.0.10").packed))
        elif name == "private.agent.test.":
            answers.append(dns_rr(name, 1, ipaddress.IPv4Address("127.0.0.1").packed))
        elif name == "cname.agent.test.":
            answers.append(dns_rr(name, 5, encode_dns_name("private.agent.test.")))
            answers.append(dns_rr("private.agent.test.", 1, ipaddress.IPv4Address("127.0.0.1").packed))
        elif name == "rebind.agent.test.":
            address = "11.0.0.10" if count == 1 else "127.0.0.1"
            answers.append(dns_rr(name, 1, ipaddress.IPv4Address(address).packed))
    elif qclass == 1 and qtype == 28 and name == "mixed.agent.test.":
        answers.append(dns_rr(name, 28, ipaddress.IPv6Address("fd00::10").packed))
    response_flags = 0x8180
    header = struct.pack("!HHHHHH", transaction_id, response_flags, 1, len(answers), 0, 0)
    return header + question + b"".join(answers)


class DNSUDPHandler(socketserver.BaseRequestHandler):
    def handle(self):
        packet, transport = self.request
        response = dns_response(packet)
        if response:
            transport.sendto(response, self.client_address)


class DNSTCPHandler(socketserver.BaseRequestHandler):
    def handle(self):
        length_bytes = self.request.recv(2)
        if len(length_bytes) != 2:
            return
        remaining = struct.unpack("!H", length_bytes)[0]
        chunks = bytearray()
        while remaining:
            chunk = self.request.recv(remaining)
            if not chunk:
                return
            chunks.extend(chunk)
            remaining -= len(chunk)
        response = dns_response(bytes(chunks))
        if response:
            self.request.sendall(struct.pack("!H", len(response)) + response)


class ThreadingUDPServer(socketserver.ThreadingUDPServer):
    daemon_threads = True
    allow_reuse_address = True

    def handle_error(self, _request, _client_address):
        STATE.server_error()


class ThreadingTCPServer(socketserver.ThreadingTCPServer):
    daemon_threads = True
    allow_reuse_address = True

    def handle_error(self, _request, _client_address):
        STATE.server_error()


def main():
    httpd = BoundedHTTPServer((HTTP_HOST, HTTP_PORT), FixtureHandler)
    dns_udp = ThreadingUDPServer((DNS_HOST, DNS_PORT), DNSUDPHandler)
    dns_tcp = ThreadingTCPServer((DNS_HOST, DNS_PORT), DNSTCPHandler)
    threads = [
        threading.Thread(target=httpd.serve_forever, daemon=True),
        threading.Thread(target=dns_udp.serve_forever, daemon=True),
        threading.Thread(target=dns_tcp.serve_forever, daemon=True),
    ]
    for thread in threads:
        thread.start()
    try:
        threads[0].join()
    finally:
        httpd.shutdown()
        dns_udp.shutdown()
        dns_tcp.shutdown()


if __name__ == "__main__":
    main()
