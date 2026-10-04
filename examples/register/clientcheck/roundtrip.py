"""Round-trips the register example through a client generated from its
OpenAPI document by openapi-python-client, an external generator.

The client raises on any status the document does not list, so a status on
the wire that the document omits fails this script.

    uvx openapi-python-client generate --path openapi.json --output-path $OUT --overwrite
    go run . -addr 127.0.0.1:18080 &
    uv run --with $OUT --with httpx python clientcheck/roundtrip.py http://127.0.0.1:18080 t-admin

The token writes user records, so it is an administrator's; the script also
checks that the demo token of a caller who is not one, t-user, is refused.
"""

import sys

from geta_register_example_client import AuthenticatedClient, Client
from geta_register_example_client.api.default import (
    delete_users_id,
    get_health,
    get_users,
    get_users_by_role_role,
    get_users_id,
    get_users_id_tags_index,
    patch_teams_team,
    post_users,
    post_users_id_attachments,
    put_teams_team,
    put_users_id,
    put_users_id_avatar,
)
from geta_register_example_client.models import (
    Attachments,
    Avatar,
    GetUsersByRoleRoleRole,
    GetUsersRole,
    PostUsersIdAttachmentsBody,
    Problem,
    PutUsersIdAvatarBody,
    Status,
    Tag,
    Team,
    TeamFields,
    TeamPatch,
    User,
    UserList,
    UserRole,
)
from geta_register_example_client.types import Unset


def expect(cond, what):
    if not cond:
        print("FAIL:", what)
        sys.exit(1)
    print("ok:", what)


def main():
    base = sys.argv[1]
    token = sys.argv[2] if len(sys.argv) > 2 else None
    if token:
        c = AuthenticatedClient(base_url=base, token=token, raise_on_unexpected_status=True)
    else:
        c = Client(base_url=base, raise_on_unexpected_status=True)
    public = Client(base_url=base, raise_on_unexpected_status=True)
    member = AuthenticatedClient(base_url=base, token="t-user", raise_on_unexpected_status=True)

    h = get_health.sync(client=public)
    expect(isinstance(h, Status) and h.status == "ok", "health")

    ada = User(id="rt-1", name="Ada", role=UserRole.MEMBER, tags=["x", "y"])
    r = post_users.sync_detailed(client=c, body=ada)
    expect(r.status_code == 201 and r.headers["location"] == "/users/rt-1", "create answers 201 with Location")

    r = post_users.sync_detailed(client=c, body=ada)
    expect(r.status_code == 409 and isinstance(r.parsed, Problem) and r.parsed.type_ == "/problems/user-exists",
           "duplicate create is a documented 409 problem with its type")

    admin = User(id="rt-4", name="Al", role=UserRole.ADMIN, tags=[])
    r = post_users.sync_detailed(client=c, body=admin)
    expect(r.status_code == 403 and r.parsed.type_ == "/problems/role-change",
           "a role set on create is a 403 told apart from the administrator check by its type")

    bo = User(id="rt-3", name="Bo", role=UserRole.MEMBER, tags=[])
    r = post_users.sync_detailed(client=member, body=bo)
    expect(r.status_code == 403 and isinstance(r.parsed, Problem) and r.parsed.detail == "admin only" and r.parsed.type_ == "about:blank",
           "a create by a caller who is not an administrator is a documented 403")
    r = delete_users_id.sync_detailed("rt-1", client=member)
    expect(r.status_code == 403, "a delete by a caller who is not an administrator is a documented 403")

    u = get_users_id.sync("rt-1", client=c)
    expect(isinstance(u, User) and u.name == "Ada" and u.role == UserRole.MEMBER and u.tags == ["x", "y"], "fetch decodes into the generated model")
    expect(u.created_at is not None and u.active is True, "server-set fields decode (date-time, boolean)")

    lst = get_users.sync(client=c, role=GetUsersRole.MEMBER, limit=10)
    expect(isinstance(lst, UserList) and lst.total >= 1, "list with typed query parameters")

    by = get_users_by_role_role.sync(GetUsersByRoleRoleRole.MEMBER, client=c)
    expect(isinstance(by, UserList), "typed path parameter")

    t = get_users_id_tags_index.sync("rt-1", 1, client=c)
    expect(isinstance(t, Tag) and t.tag == "y", "two path parameters, one an integer")

    r = get_users_id_tags_index.sync_detailed("rt-1", 9, client=c)
    expect(r.status_code == 404 and r.parsed.detail == "tag index out of range", "a failure row reaches the client as a problem")

    bad = User(id="rt-2", name="Ada", role=UserRole.MEMBER, tags=[])
    r = put_users_id.sync_detailed("rt-1", client=c, body=bad)
    expect(r.status_code == 400, "a mismatched id is a documented 400")

    tag = get_users_id.sync_detailed("rt-1", client=c).headers["etag"]
    r = get_users_id.sync_detailed("rt-1", client=c, if_none_match=tag)
    expect(r.status_code == 304, "a fetch holding the current ETag is a documented 304")
    renamed = User(id="rt-1", name="Ada B", role=UserRole.MEMBER, tags=[])
    r = put_users_id.sync_detailed("rt-1", client=c, body=renamed, if_match=tag)
    expect(r.status_code == 200 and isinstance(r.parsed, User) and r.parsed.name == "Ada B" and r.headers["etag"] != tag,
           "a replace naming the tag held round-trips the model and its new tag")
    r = put_users_id.sync_detailed("rt-1", client=c, body=renamed, if_match=tag)
    expect(r.status_code == 412, "a replace naming a stale tag is a documented 412")

    # The generator reads the file property (type string, contentMediaType
    # application/octet-stream) as a string and sends it as a text part with
    # no filename; the field, a geta.File, takes the part as the file.
    r = put_users_id_avatar.sync_detailed("rt-1", client=c, body=PutUsersIdAvatarBody(image="PNG", caption="me"))
    expect(r.status_code == 200 and isinstance(r.parsed, Avatar) and r.parsed.bytes_ == 3 and r.parsed.caption == "me", "a multipart upload round-trips")
    r = put_users_id_avatar.sync_detailed("rt-9", client=c, body=PutUsersIdAvatarBody(image="PNG"))
    expect(r.status_code == 404 and isinstance(r.parsed, Problem), "an upload for no user is a documented 404")
    r = c.get_httpx_client().put("/users/rt-1/avatar", content=b"PNG", headers={"Content-Type": "image/png"})
    expect(r.status_code == 415 and r.headers["accept"] == "multipart/form-data", "a body of another media type is a documented 415 naming the one taken")

    r = get_users.sync_detailed(client=c, limit=1000)
    expect(r.status_code == 400 and isinstance(r.parsed, Problem), "a constraint violation is a documented 400")

    # Several files under one name are a list of files to the generator, as
    # []geta.File is to geta.
    r = post_users_id_attachments.sync_detailed("rt-1", client=c, body=PostUsersIdAttachmentsBody(file=["a,b", "c"], note="two"))
    expect(r.status_code == 200 and isinstance(r.parsed, Attachments) and [a.bytes_ for a in r.parsed.items] == [3, 1]
           and r.parsed.note == "two", "a multipart upload of several files round-trips")

    # PUT creates or replaces, as the precondition says; PATCH is a merge
    # patch whose description takes null.
    r = put_teams_team.sync_detailed("rt", client=c, body=TeamFields(name="Round trip", description="first"), if_none_match="*")
    expect(r.status_code == 200 and isinstance(r.parsed, Team) and r.parsed.description == "first", "a PUT with If-None-Match: * creates")
    tag = r.headers["etag"]
    r = put_teams_team.sync_detailed("rt", client=c, body=TeamFields(name="Again"), if_none_match="*")
    expect(r.status_code == 412, "a second create is a documented 412")
    r = patch_teams_team.sync_detailed("rt", client=c, body=TeamPatch(name="Renamed"), if_match=tag)
    expect(r.status_code == 200 and r.parsed.name == "Renamed" and r.parsed.description == "first", "a patch keeps what it leaves out")
    r = patch_teams_team.sync_detailed("rt", client=c, body=TeamPatch(description=None))
    expect(r.status_code == 200 and isinstance(r.parsed.description, Unset), "a patch with a null description clears it")
    r = patch_teams_team.sync_detailed("rt", client=c, body=TeamPatch(description="back"), if_match=tag)
    expect(r.status_code == 412, "a patch of a stale version is a documented 412")

    r = delete_users_id.sync_detailed("rt-1", client=c)
    expect(r.status_code == 204, "delete answers 204")
    r = delete_users_id.sync_detailed("rt-1", client=c)
    expect(r.status_code == 404, "a second delete is a documented 404")
    print("round trip: all checks passed")


if __name__ == "__main__":
    main()
