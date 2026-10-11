#!/usr/bin/env python3
"""Reproduce the upstream retained-account disable gap on disposable native data.

This is a counterexample check, not a production adapter or successful admission.
"""
import base64
import contextlib
import concurrent.futures
import hashlib
import html.parser
import http.cookiejar
import json
import os
import pathlib
import re
import secrets
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


class Fields(html.parser.HTMLParser):
    def __init__(self, page):
        super().__init__()
        self.named = {}
        self.readonly = []
        self.feed(page.decode())

    def handle_starttag(self, tag, attrs):
        if tag != "input":
            return
        attrs = dict(attrs)
        if "name" in attrs:
            if attrs["name"] not in {"_token", "token_id"}:
                return
            value = attrs.get("value", "")
            previous = self.named.get(attrs["name"])
            if previous is not None and previous != value:
                raise AssertionError("ambiguous fixture form")
            self.named[attrs["name"]] = value
        elif "readonly" in attrs:
            self.readonly.append(attrs.get("value", ""))


class Web:
    def __init__(self, origin):
        self.origin = origin
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
        )

    def request(self, path, data=None, method=None, token=None):
        headers = {"Origin": self.origin}
        if token:
            headers["Authorization"] = "Token " + token
        if isinstance(data, dict):
            body = json.dumps(data).encode()
            headers["Content-Type"] = "application/json"
        elif data is not None:
            body = urllib.parse.urlencode(data).encode()
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        else:
            body = None
        req = urllib.request.Request(self.origin + path, body, headers, method=method)
        with self.opener.open(req, timeout=5) as response:
            if not response.url.startswith(self.origin + "/"):
                raise AssertionError("fixture escaped its loopback origin")
            page = response.read((1 << 20) + 1)
            if len(page) > 1 << 20:
                raise AssertionError("oversized fixture response")
            return page

    def login(self, email, password):
        form = Fields(self.request("/login")).named
        if "_token" not in form:
            raise AssertionError("native login form absent")
        self.request("/login", [("_token", form["_token"]), ("email", email), ("password", password)])

    def token(self, user_id):
        path = "/api-tokens/" + str(user_id) + "/create"
        fields = Fields(self.request(path)).named
        result = Fields(self.request(path, [("_token", fields["_token"]), ("name", "disposable-native-qualification")]))
        token_id = result.named.get("token_id", "")
        if len(token_id) != 32 or len(result.readonly) != 1 or len(result.readonly[0]) != 32:
            raise AssertionError("native token receipt mismatch: named fields " + str(sorted(result.named)) + "; unnamed readonly lengths " + str([len(value) for value in result.readonly]))
        return token_id + ":" + result.readonly[0]


def docker(*args):
    result = subprocess.run(["docker", *args], capture_output=True, timeout=120)
    if result.returncode:
        raise RuntimeError("disposable Docker operation " + args[0] + " failed")
    return result.stdout.decode().strip()


def pinned(value):
    return re.fullmatch(r"[^\s]+@sha256:[0-9a-f]{64}", value) is not None


def cleanup(*args):
    subprocess.run(["docker", *args], capture_output=True, timeout=30)


def diagnose(app):
    if sys.exc_info()[0] is None:
        return
    result = subprocess.run(["docker", "exec", app, "cat", "/app/www/storage/logs/laravel.log"],
                            capture_output=True, timeout=10)
    # Report exception classes and function locations, never raw native messages/bodies.
    log = result.stdout.decode(errors="replace")
    classes = re.findall(r"\[object\] \(([^ (]+)\(code:", log)
    frames = re.findall(r"#\d+ ([^\n(]+)\((\d+)\): ([^\n(]+)", log.rsplit('[stacktrace]', 1)[-1])
    print("Native diagnostic classes:", classes[-3:])
    print("Native diagnostic frames:", frames[:5])
    print("Native role refusal categories:", re.findall(r'"reason":"(ordinary-policy|initialization|binding|request|drift|audit|native-error)"', log)[-5:])
    result = subprocess.run(["docker", "exec", app, "sh", "-c",
                             "id abc; ls -ld /app/www/storage /app/www/storage/framework /app/www/storage/logs /config/www; readlink -f /app/www/storage/logs; readlink -f /app/www/storage/framework/sessions"],
                            capture_output=True, timeout=10)
    print("Native fixture ownership:", result.stdout.decode().strip())


def denied(web, path, **kwargs):
    try:
        web.request(path, **kwargs)
    except urllib.error.HTTPError as error:
        if error.code in (401, 403):
            return
        raise
    raise AssertionError("revoked credential still authorizes " + path)


def offboard(app, request, reject=False, private=True, command="kyquickstart:offboard"):
    # Only the freshly owned application container receives this private fixture file.
    encoded = base64.b64encode(json.dumps(request).encode()).decode()
    path = "/config/kyq-fixture/request-" + secrets.token_hex(8) + ".json"
    docker("exec", "-u", "abc", app, "sh", "-c",
           "umask 077; mkdir -p /config/kyq-fixture; printf '%s' '" + encoded +
           "' | base64 -d > " + path)
    if not private:
        docker("exec", "-u", "abc", app, "chmod", "644", path)
    try:
        result = subprocess.run([
            "docker", "exec", "-u", "abc", "-w", "/app/www", app, "php", "artisan",
            command, "--request=" + path,
        ], capture_output=True, timeout=30)
    finally:
        cleanup("exec", "-u", "abc", app, "rm", "-f", path)
    if reject:
        expected = {"kyquickstart:offboard": b"Offboarding rejected",
                    "kyquickstart:profile": b"Profile request rejected",
                    "kyquickstart:roles": b"Role request rejected",
                    "kyquickstart:directory": b"Directory request rejected"}[command]
        if result.returncode != 1 or expected not in result.stdout + result.stderr:
            raise AssertionError("native command did not reject invalid binding")
        return
    if result.returncode:
        raise AssertionError("native offboarding command failed")
    return json.loads(result.stdout)


def main():
    app_image = os.environ.get("KYQ_TEST_BOOKSTACK_IMAGE", "")
    db_image = os.environ.get("KYQ_TEST_BOOKSTACK_DB_IMAGE", "")
    if not pinned(app_image) or not pinned(db_image):
        raise AssertionError("set digest-pinned BookStack and MariaDB fixture images")
    suffix = secrets.token_hex(6)
    network, db, app = ["kyq-bookstack-" + suffix + ending for ending in ("-net", "-db", "-app")]
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    origin = "http://127.0.0.1:" + str(port)
    adapter = os.environ.get("KYQ_TEST_BOOKSTACK_ADAPTER") == "1"
    module = os.environ.get("KYQ_TEST_BOOKSTACK_MODULE") == "1" or adapter
    with contextlib.ExitStack() as owned:
        docker("network", "create", network)
        owned.callback(cleanup, "network", "rm", network)
        docker("run", "-d", "--name", db, "--network", network, "--memory", "768m", "--memory-swap", "768m", "--cpus", "1", "--cap-drop", "NET_RAW",
               "-e", "MARIADB_ROOT_PASSWORD=disposable-fixture-root-password", "-e", "MARIADB_DATABASE=bookstack",
               "-e", "MARIADB_USER=bookstack", "-e", "MARIADB_PASSWORD=disposable-fixture-db-password", db_image)
        owned.callback(cleanup, "rm", "-fv", db)
        docker("run", "-d", "--name", app, "--network", network, "--memory", "1g", "--memory-swap", "1g", "--cpus", "1", "--cap-drop", "NET_RAW",
               "--label", "kyq.fixture=bookstack", "--label", "kyq.fixture-owner=" + suffix,
               "-p", "127.0.0.1:" + str(port) + ":80", "-e", "APP_URL=" + origin,
               "-e", "APP_KEY=base64:" + base64.b64encode(secrets.token_bytes(32)).decode(),
               "-e", "DB_HOST=" + db, "-e", "DB_DATABASE=bookstack", "-e", "DB_USERNAME=bookstack",
               "-e", "DB_PASSWORD=disposable-fixture-db-password", "-e", "AUTH_METHOD=standard",
               "-e", "OIDC_ISSUER=https://identity.example.test",
               "-e", "OIDC_CLIENT_ID=fixture-client", "-e", "OIDC_USER_TO_GROUPS=true",
               "-e", "OIDC_GROUPS_CLAIM=roles", "-e", "OIDC_REMOVE_FROM_GROUPS=true", "-e", "AVATAR_URL=false",
               "-e", "KYQ_BOOKSTACK_REVOCATION_DIR=/config/kyquickstart-revocations",
               "-e", "APP_THEME=" + ("kyquickstart" if module else ""), app_image)
        owned.callback(cleanup, "rm", "-fv", app)
        owned.callback(diagnose, app)
        web = Web(origin)
        deadline = time.monotonic() + 120
        while True:
            try:
                page = web.request("/login")
                if "_token" in Fields(page).named:
                    break
            except (OSError, urllib.error.URLError):
                pass
            if time.monotonic() >= deadline:
                raise AssertionError("disposable BookStack startup timed out")
            time.sleep(0.25)
        native_source = docker("exec", app, "cat", "/app/www/app/Users/Controllers/UserApiController.php")
        print("Native UserApiController SHA-256:", hashlib.sha256(native_source.encode()).hexdigest())
        if module:
            module_path = str(pathlib.Path(__file__).resolve().parent / "module")
            docker("exec", "-u", "abc", app, "mkdir", "-m", "700", "/config/kyquickstart-revocations")
            docker("exec", app, "mkdir", "-p", "/config/www/themes/kyquickstart/modules")
            docker("cp", module_path, app + ":/config/www/themes/kyquickstart/modules/offboarding")
            docker("exec", app, "php", "-l", "/config/www/themes/kyquickstart/modules/offboarding/functions.php")
            docker("exec", app, "php", "-l", "/config/www/themes/kyquickstart/modules/offboarding/accounts.php")
            docker("exec", app, "php", "-l", "/config/www/themes/kyquickstart/modules/offboarding/profiles.php")
            docker("exec", app, "php", "-l", "/config/www/themes/kyquickstart/modules/offboarding/roles.php")
            docker("exec", app, "php", "-l", "/config/www/themes/kyquickstart/modules/offboarding/directory.php")
        web.login("admin@admin.com", "password")
        token = web.token(1)
        api = Web(origin)
        role = json.loads(api.request("/api/roles", {
            "display_name": "Qualification reader", "permissions": ["access-api", "book-view-all", "chapter-view-all", "page-view-all", "book-create-all"]
        }, method="POST", token=token))
        user = json.loads(api.request("/api/users", {
            "name": "Everyday qualification", "email": "everyday@example.test", "password": "disposable-user-password",
            "external_auth_id": "disposable-bound-subject", "roles": [role["id"]]
        }, method="POST", token=token))
        user_id = user["id"]
        old_token = web.token(user_id)
        daily = Web(origin)
        daily.login("everyday@example.test", "disposable-user-password")
        json.loads(daily.request("/api/books"))
        json.loads(Web(origin).request("/api/books", token=old_token))
        result = json.loads(api.request("/api/users/" + str(user_id), {"active": False, "disabled": True}, method="PUT", token=token))
        if result["id"] != user_id or result["external_auth_id"] != "disposable-bound-subject" or result["roles"][0]["id"] != role["id"]:
            raise AssertionError("upstream disable behavior changed; qualify the new interface")
        json.loads(daily.request("/api/books"))
        json.loads(Web(origin).request("/api/books", token=old_token))
        print("REPRODUCED: disable flags ignored; existing session and API token still authorize.")
        if not module:
            print("BookStack admission remains gated on a native retained-account offboarding interface.")
            return
        book = json.loads(Web(origin).request("/api/books", {
            "name": "Retained qualification content", "description": "Content survives account revocation"
        }, method="POST", token=old_token))
        binding = {"contract": "ky.bookstack-offboard/1", "action": "check", "user_id": user_id,
                   "issuer": "https://identity.example.test", "subject": "disposable-bound-subject"}
        before = offboard(app, binding)
        if before["disabled"] or before["tokens_remaining"] != 1:
            raise AssertionError("initial native binding receipt mismatch")
        offboard(app, {**binding, "action": "deprovision", "subject": "wrong-subject"}, reject=True)
        offboard(app, {**binding, "action": "deprovision", "issuer": "https://wrong.example.test"}, reject=True)
        offboard(app, {**binding, "action": "deprovision"}, reject=True, private=False)
        offboard(app, {**binding, "action": "deprovision", "subject": "x" * 5000}, reject=True)
        if offboard(app, binding) != before:
            raise AssertionError("rejected request mutated authority")
        if adapter:
            native_env = {**os.environ, "KYQ_TEST_BOOKSTACK_CONTAINER": app,
                          "KYQ_TEST_BOOKSTACK_FIXTURE_OWNER": suffix, "KYQ_TEST_BOOKSTACK_USER_ID": str(user_id)}
            native = subprocess.run(["go", "test", "-race", "-v", "-count=1", "./internal/adapters/bookstack",
                                     "-run", "^TestNativeOffboardingAdapter$"],
                                    capture_output=True, timeout=240, env=native_env)
            print(native.stdout.decode().strip())
            if native.returncode:
                raise AssertionError("native Go adapter qualification failed")
            users = json.loads(api.request("/api/users", token=token))["data"]
            owners = [user for user in users if user.get("external_auth_id") == "setup-owner-subject"]
            if len(owners) != 1:
                raise AssertionError("owned setup profile ambiguous")
            owner_id = owners[0]["id"]
            changed = {"name": "Retained owner 雪", "email": "retained-owner@example.test"}
            api.request("/api/users/" + str(owner_id), changed, method="PUT", token=token)
            original = {"contract": "ky.bookstack-account/1", "action": "check",
                        "setup_id": "2c676879-9c71-4913-b8a7-995521d9ed20",
                        "issuer": "https://identity.example.test", "client_id": "fixture-client",
                        "subject": "setup-owner-subject", "name": "Owner 雪 & <test>",
                        "email": "setup-owner@example.test", "administrator": True}
            for action in ("check", "create", "check"):
                report = offboard(app, {**original, "action": action}, command="kyquickstart:account")
                if (report.get("contract") != "ky.bookstack-account-report/2" or
                        report["user_id"] != owner_id or report["created"] or report["disabled"] or
                        report.get("name") != changed["name"] or report.get("email") != changed["email"]):
                    raise AssertionError("setup inspection/resume reset or concealed retained profile")
            print("PASS: native setup readback/resume preserves operator profile edits and exact ownership.")
            native = subprocess.run(["go", "test", "-race", "-v", "-count=1", "./internal/adapters/bookstack",
                                     "-run", "^TestNativeOffboardingAdapter$"], capture_output=True, timeout=240,
                                    env={**native_env, "KYQ_TEST_BOOKSTACK_PROFILE": "1"})
            print(native.stdout.decode().strip())
            if native.returncode:
                raise AssertionError("native owned profile adapter qualification failed")
            before_guard = json.loads(api.request("/api/users/" + str(owner_id), token=token))
            try:
                api.request("/api/users/" + str(owner_id), {"name": "Foreign native edit"}, method="PUT", token=token)
                raise AssertionError("native writer replaced directory-owned profile")
            except urllib.error.HTTPError as error:
                if error.code != 403:
                    raise
            try:
                api.request("/api/users/" + str(owner_id), method="DELETE", token=token)
                raise AssertionError("native writer deleted directory-owned identity")
            except urllib.error.HTTPError as error:
                if error.code != 403:
                    raise
            after_guard = json.loads(api.request("/api/users/" + str(owner_id), token=token))
            if after_guard["name"] != before_guard["name"] or after_guard["email"] != before_guard["email"]:
                raise AssertionError("rejected native writer changed owned profile")
            print("PASS: native profile writer cannot overwrite projected owned name/email.")

            receipt = offboard(app, binding)
        else:
            receipt = offboard(app, {**binding, "action": "deprovision"})
        if not receipt["disabled"] or receipt["tokens_remaining"] != 0 or receipt["user_id"] != user_id:
            raise AssertionError("revocation receipt mismatch")
        denied(daily, "/api/books")
        denied(daily, "/books")
        denied(Web(origin), "/api/books", token=old_token)
        try:
            Web(origin).login("everyday@example.test", "disposable-user-password")
        except urllib.error.HTTPError as error:
            if error.code != 403:
                raise
        else:
            raise AssertionError("revoked account signed in again")
        retained_user = json.loads(api.request("/api/users/" + str(user_id), token=token))
        retained_book = json.loads(api.request("/api/books/" + str(book["id"]), token=token))
        if retained_user["id"] != user_id or retained_user["external_auth_id"] != binding["subject"] or retained_user["roles"] != result["roles"]:
            raise AssertionError("offboarding changed native identity or roles")
        if retained_book["id"] != book["id"] or retained_book["description"] != book["description"]:
            raise AssertionError("offboarding lost owned content")
        replacement_token = web.token(user_id)
        denied(Web(origin), "/api/books", token=replacement_token)
        offboard(app, {**binding, "action": "deprovision"})
        api.request("/api/users/" + str(user_id), {"external_auth_id": "rebound-subject"}, method="PUT", token=token)
        denied(daily, "/api/books")
        offboard(app, {**binding, "action": "deprovision", "subject": "rebound-subject"}, reject=True)
        api.request("/api/users/" + str(user_id), {"external_auth_id": binding["subject"]}, method="PUT", token=token)
        alias = json.loads(api.request("/api/users", {
            "name": "Recreated subject fixture", "email": "recreated@example.test", "password": "disposable-user-password",
            "external_auth_id": binding["subject"], "roles": [role["id"]]
        }, method="POST", token=token))
        if alias["id"] == user_id:
            raise AssertionError("subject recreation fixture failed")
        alias_token = web.token(alias["id"])
        denied(Web(origin), "/api/books", token=alias_token)
        try:
            Web(origin).login("recreated@example.test", "disposable-user-password")
        except urllib.error.HTTPError as error:
            if error.code != 403:
                raise
        else:
            raise AssertionError("recreated subject signed in")
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as workers:
            repeated = list(workers.map(lambda _: offboard(app, {**binding, "action": "deprovision"}), range(2)))
        if repeated != [receipt, receipt]:
            raise AssertionError("concurrent native revocation changed receipt")
        docker("restart", app)
        deadline = time.monotonic() + 90
        while True:
            try:
                api.request("/api/books", token=token)
                break
            except (OSError, urllib.error.URLError):
                if time.monotonic() >= deadline:
                    raise AssertionError("native module restart timed out")
                time.sleep(0.25)
        if offboard(app, binding) != receipt:
            raise AssertionError("restart lost persistent revocation")
        denied(daily, "/api/books")
        denied(Web(origin), "/api/books", token=old_token)
        print("PASS: native module denies old web/API sessions, old API token and new password login.")
        print("PASS: exact binding/private-file refusal, retained account/roles/content, concurrent retry and restart persistence.")
        print("PASS: replacement token, changed subject on retained ID and recreated subject on a new ID remain denied.")
        print("Separate signed/native-provider OIDC fixtures exist; resident offboarding, restore, installer orchestration and release provenance remain gates.")


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    try:
        main()
    except Exception as error:
        print("Native BookStack qualification failed:", type(error).__name__, file=sys.stderr)
        if isinstance(error, AssertionError):
            print(str(error), file=sys.stderr)
        if isinstance(error, urllib.error.HTTPError):
            print("Native HTTP status:", error.code, file=sys.stderr)
        sys.exit(1)
