"""Round-trips the auth example through a client generated from its OpenAPI
document by openapi-python-client, an external generator.

The client raises on any status the document does not list, so a status on
the wire that the document omits fails this script.

    uvx openapi-python-client generate --path openapi.json --output-path $OUT --overwrite
    go run . -addr 127.0.0.1:18280 &
    uv run --with $OUT --with httpx python clientcheck/roundtrip.py http://127.0.0.1:18280
"""

import sys
import threading

import httpx
from geta_auth_example_client import AuthenticatedClient, Client
from geta_auth_example_client.api.default import get_admin_whoami, get_me, get_public, post_login, post_logout
from geta_auth_example_client.models import Done, Identity, Message, PostLoginBody, Problem, Role, SessionRole


def expect(cond, what):
    if not cond:
        print("FAIL:", what)
        sys.exit(1)
    print("ok:", what)


def main():
    base = sys.argv[1]
    anon = Client(base_url=base, raise_on_unexpected_status=True)

    p = get_public.sync(client=anon)
    expect(isinstance(p, Message), "a route that declares itself public")
    r = get_admin_whoami.sync_detailed(client=anon)
    expect(r.status_code == 401 and isinstance(r.parsed, Problem) and r.headers["www-authenticate"] == "Bearer",
           "no credentials is a documented 401 with a bare challenge")
    member = AuthenticatedClient(base_url=base, token="member-token", raise_on_unexpected_status=True)
    r = get_admin_whoami.sync_detailed(client=member)
    expect(r.status_code == 403 and r.parsed.detail == 'requires the "admin" role', "the wrong role is a documented 403")
    admin = AuthenticatedClient(base_url=base, token="admin-token", raise_on_unexpected_status=True)
    w = get_admin_whoami.sync(client=admin)
    expect(isinstance(w, Identity) and w.role == "admin", "the right bearer token")

    # The login is a form (application/x-www-form-urlencoded).
    r = post_login.sync_detailed(client=anon, body=PostLoginBody(username="admin", password="wrong"))
    expect(r.status_code == 401 and isinstance(r.parsed, Problem), "a wrong password is a documented 401")
    r = anon.get_httpx_client().post("/login", json={"username": "admin", "password": "admin-pass"})
    expect(r.status_code == 415 and r.headers["accept"] == "application/x-www-form-urlencoded", "JSON is a 415 naming the form")
    r = post_login.sync_detailed(client=anon, body=PostLoginBody(username="admin", password="admin-pass"))
    expect(r.status_code == 200 and isinstance(r.parsed, Role) and r.parsed.role == "admin", "log in with the form")
    cookie = r.headers["set-cookie"]
    expect(cookie.startswith("sid=") and "HttpOnly" in cookie and "SameSite=Lax" in cookie, "the session cookie's attributes")
    sid = cookie.split(";")[0].removeprefix("sid=")

    session = Client(base_url=base, cookies={"sid": sid}, raise_on_unexpected_status=True)
    me = get_me.sync(client=session)
    expect(isinstance(me, SessionRole) and me.role == "admin", "the cookie session")
    r = get_admin_whoami.sync_detailed(client=session)
    expect(r.status_code == 401, "a cookie does not open the bearer-only /admin")

    # The session feed is an event stream; logging out ends it from the
    # server's side with one revoked event.
    events = []
    opened = threading.Event()

    def listen():
        with httpx.stream("GET", base + "/me/events", cookies={"sid": sid}, timeout=10) as res:
            events.append(res.status_code)
            opened.set()
            for line in res.iter_lines():
                if line.startswith("event:") or line.startswith("data:"):
                    events.append(line)

    t = threading.Thread(target=listen)
    t.start()
    expect(opened.wait(5), "the feed opened")
    r = post_logout.sync_detailed(client=session)
    expect(r.status_code == 200 and isinstance(r.parsed, Done) and r.headers["set-cookie"].startswith("sid=; Path=/; Max-Age=0"),
           "log out expires the cookie")
    t.join(10)
    expect(not t.is_alive() and events == [200, "event: revoked", 'data: {"kind":"revoked"}'], "the feed ends with a revoked event")
    r = get_me.sync_detailed(client=session)
    expect(r.status_code == 401, "the session is gone")
    print("round trip: all checks passed")


if __name__ == "__main__":
    main()
