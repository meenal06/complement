// +build msc2946,msc3083

// Tests MSC3083, an experimental feature for joining restricted rooms based on
// membership in a space.

package tests

import (
	"net/url"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/matrix-org/complement/internal/b"
	"github.com/matrix-org/complement/internal/client"
	"github.com/matrix-org/complement/internal/docker"
	"github.com/matrix-org/complement/internal/match"
	"github.com/matrix-org/complement/internal/must"
)

func failJoinRoom(t *testing.T, c *client.CSAPI, roomIDOrAlias string, serverName string, expectedErrorCode int) {
	t.Helper()

	// This is copied from Client.JoinRoom to test a join failure.
	query := make(url.Values, 1)
	query.Set("server_name", serverName)
	res := c.DoFunc(
		t,
		"POST",
		[]string{"_matrix", "client", "r0", "join", roomIDOrAlias},
		client.WithQueries(query),
	)
	must.MatchResponse(t, res, match.HTTPResponse{
		StatusCode: expectedErrorCode,
	})
}

// Create a space and put a room in it which is set to:
// * The experimental room version.
// * restricted join rules with allow set to the space.
func setupRestrictedRoom(t *testing.T, deployment *docker.Deployment) (*client.CSAPI, string, string) {
	t.Helper()

	alice := deployment.Client(t, "hs1", "@alice:hs1")
	space := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"name":   "Space",
		"creation_content": map[string]interface{}{
			"type": "m.space",
		},
	})
	// The room is an unstable room version which supports the restricted join_rule.
	room := alice.CreateRoom(t, map[string]interface{}{
		"preset":       "public_chat",
		"name":         "Room",
		"room_version": "8",
		"initial_state": []map[string]interface{}{
			{
				"type":      "m.room.join_rules",
				"state_key": "",
				"content": map[string]interface{}{
					"join_rule": "restricted",
					"allow": []map[string]interface{}{
						{
							"type":    "m.room_membership",
							"room_id": &space,
							"via":     []string{"hs1"},
						},
					},
				},
			},
		},
	})
	alice.SendEventSynced(t, space, b.Event{
		Type:     "m.space.child",
		StateKey: &room,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})

	return alice, space, room
}

func checkRestrictedRoom(t *testing.T, alice *client.CSAPI, bob *client.CSAPI, space string, room string) {
	t.Helper()

	failJoinRoom(t, bob, room, "hs1", 403)

	// Join the space, attempt to join the room again, which now should succeed.
	bob.JoinRoom(t, space, []string{"hs1"})
	bob.JoinRoom(t, room, []string{"hs1"})

	// Joining the same room again should work fine (e.g. to change your display name).
	bob.JoinRoom(t, room, []string{"hs1"})

	// Leaving the room works and the user is unable to re-join.
	bob.LeaveRoom(t, room)
	bob.LeaveRoom(t, space)

	// Wait until Alice sees Bob leave the space. This ensures that Alice's HS
	// has processed the leave before Bob tries rejoining, so that it rejects his
	// attempt to join the room.
	alice.SyncUntilTimelineHas(t, space, func(ev gjson.Result) bool {
		if ev.Get("type").Str != "m.room.member" || ev.Get("sender").Str != bob.UserID {
			return false
		}

		return ev.Get("content").Get("membership").Str == "leave"
	})

	failJoinRoom(t, bob, room, "hs1", 403)

	// Invite the user and joining should work.
	alice.InviteRoom(t, room, bob.UserID)
	bob.JoinRoom(t, room, []string{"hs1"})

	// Leave the room again, and join the space.
	bob.LeaveRoom(t, room)
	bob.JoinRoom(t, space, []string{"hs1"})

	// Update the room to have bad values in the "allow" field, which should stop
	// joining from working properly.
	emptyStateKey := ""
	alice.SendEventSynced(
		t,
		room,
		b.Event{
			Type:     "m.room.join_rules",
			Sender:   alice.UserID,
			StateKey: &emptyStateKey,
			Content: map[string]interface{}{
				"join_rule": "restricted",
				"allow":     []string{"invalid"},
			},
		},
	)
	// Fails since invalid values get filtered out of allow.
	failJoinRoom(t, bob, room, "hs1", 403)

	alice.SendEventSynced(
		t,
		room,
		b.Event{
			Type:     "m.room.join_rules",
			Sender:   alice.UserID,
			StateKey: &emptyStateKey,
			Content: map[string]interface{}{
				"join_rule": "restricted",
				"allow":     "invalid",
			},
		},
	)
	// Fails since a fully invalid allow key requires an invite.
	failJoinRoom(t, bob, room, "hs1", 403)
}

// Test joining a room with join rules restricted to membership in a space.
func TestRestrictedRoomsLocalJoin(t *testing.T) {
	deployment := Deploy(t, b.BlueprintOneToOneRoom)
	defer deployment.Destroy(t)

	// Setup the user, space, and restricted room.
	alice, space, room := setupRestrictedRoom(t, deployment)

	// Create a second user on the same homeserver.
	bob := deployment.Client(t, "hs1", "@bob:hs1")

	// Execute the checks.
	checkRestrictedRoom(t, alice, bob, space, room)
}

// Test joining a room with join rules restricted to membership in a space.
func TestRestrictedRoomsRemoteJoin(t *testing.T) {
	deployment := Deploy(t, b.BlueprintFederationOneToOneRoom)
	defer deployment.Destroy(t)

	// Setup the user, space, and restricted room.
	alice, space, room := setupRestrictedRoom(t, deployment)

	// Create a second user on a different homeserver.
	bob := deployment.Client(t, "hs2", "@bob:hs2")

	// Execute the checks.
	checkRestrictedRoom(t, alice, bob, space, room)
}

// A server will do a remote join for a local user if it is unable to to issue
// joins in a restricted room it is already participating in.
func TestRestrictedRoomsRemoteJoinLocalUser(t *testing.T) {
	deployment := Deploy(t, b.BlueprintFederationTwoLocalOneRemote)
	defer deployment.Destroy(t)

	// Charlie sets up the space so it is on the other server.
	charlie := deployment.Client(t, "hs2", "@charlie:hs2")
	space := charlie.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"name":   "Space",
		"creation_content": map[string]interface{}{
			"type": "m.space",
		},
	})
	// The room is an unstable room version which supports the restricted join_rule.
	room := charlie.CreateRoom(t, map[string]interface{}{
		"preset":       "public_chat",
		"name":         "Room",
		"room_version": "8",
		"initial_state": []map[string]interface{}{
			{
				"type":      "m.room.join_rules",
				"state_key": "",
				"content": map[string]interface{}{
					"join_rule": "restricted",
					"allow": []map[string]interface{}{
						{
							"type":    "m.room_membership",
							"room_id": &space,
							"via":     []string{"hs2"},
						},
					},
				},
			},
		},
	})
	charlie.SendEventSynced(t, space, b.Event{
		Type:     "m.space.child",
		StateKey: &room,
		Content: map[string]interface{}{
			"via": []string{"hs2"},
		},
	})

	// Invite alice manually and accept it.
	alice := deployment.Client(t, "hs1", "@alice:hs1")
	charlie.InviteRoom(t, room, alice.UserID)
	alice.JoinRoom(t, room, []string{"hs2"})

	// Confirm that Alice cannot issue invites (due to the default power levels).
	bob := deployment.Client(t, "hs1", "@bob:hs1")
	body := map[string]interface{}{
		"user_id": bob.UserID,
	}
	res := alice.DoFunc(
		t,
		"POST",
		[]string{"_matrix", "client", "r0", "rooms", room, "invite"},
		client.WithJSONBody(t, body),
	)
	must.MatchResponse(t, res, match.HTTPResponse{
		StatusCode: 403,
	})

	// Bob cannot join the room.
	failJoinRoom(t, bob, room, "hs1", 403)

	// Join the space via hs2.
	bob.JoinRoom(t, space, []string{"hs2"})
	// Joining the room should work, although we're joining via hs1, it will end up
	// as a remote join through hs2.
	bob.JoinRoom(t, room, []string{"hs1"})

	// Ensure that the join comes down sync on hs2. Note that we want to ensure hs2
	// accepted the event.
	charlie.SyncUntilTimelineHas(
		t,
		room,
		func(ev gjson.Result) bool {
			if ev.Get("type").Str != "m.room.member" || ev.Get("state_key").Str != bob.UserID {
				return false
			}
			must.EqualStr(t, ev.Get("sender").Str, bob.UserID, "Bob should have joined by himself")
			must.EqualStr(t, ev.Get("content").Get("membership").Str, "join", "Bob failed to join the room")

			return true
		},
	)

	// Raise the power level so that users on hs1 can invite people and then leave
	// the room.
	state_key := ""
	charlie.SendEventSynced(t, room, b.Event{
		Type:     "m.room.power_levels",
		StateKey: &state_key,
		Content: map[string]interface{}{
			"invite": 0,
			"users": map[string]interface{}{
				charlie.UserID: 100,
			},
		},
	})
	charlie.LeaveRoom(t, room)

	// Ensure the events have synced to hs1.
	alice.SyncUntilTimelineHas(
		t,
		room,
		func(ev gjson.Result) bool {
			if ev.Get("type").Str != "m.room.member" || ev.Get("state_key").Str != charlie.UserID {
				return false
			}
			must.EqualStr(t, ev.Get("content").Get("membership").Str, "leave", "Charlie failed to leave the room")

			return true
		},
	)

	// Have bob leave and rejoin. This should still work even though hs2 isn't in
	// the room anymore!
	bob.LeaveRoom(t, room)
	bob.JoinRoom(t, room, []string{"hs1"})
}

// A server will request a failover if asked to /make_join and it does not have
// the appropriate authorisation to complete the request.
//
// Setup 3 homeservers:
// * hs1 creates the space/room.
// * hs2 joins the room
// * hs3 attempts to join via hs2 (should fail) and hs1 (should work)
func TestRestrictedRoomsRemoteJoinFailOver(t *testing.T) {
	deployment := Deploy(t, b.Blueprint{
		Name: "federation_three_homeservers",
		Homeservers: []b.Homeserver{
			{
				Name: "hs1",
				Users: []b.User{
					{
						Localpart:   "alice",
						DisplayName: "Alice",
					},
				},
			},
			{
				Name: "hs2",
				Users: []b.User{
					{
						Localpart:   "bob",
						DisplayName: "Bob",
					},
				},
			},
			{
				Name: "hs3",
				Users: []b.User{
					{
						Localpart:   "charlie",
						DisplayName: "Charlie",
					},
				},
			},
		},
	})
	defer deployment.Destroy(t)

	// Setup the user, space, and restricted room.
	alice, space, room := setupRestrictedRoom(t, deployment)

	// Raise the power level so that only alice can invite.
	state_key := ""
	alice.SendEventSynced(t, room, b.Event{
		Type:     "m.room.power_levels",
		StateKey: &state_key,
		Content: map[string]interface{}{
			"invite": 100,
			"users": map[string]interface{}{
				alice.UserID: 100,
			},
		},
	})

	// Create a second user on a different homeserver.
	bob := deployment.Client(t, "hs2", "@bob:hs2")

	// Bob joins the room and space.
	bob.JoinRoom(t, space, []string{"hs1"})
	bob.JoinRoom(t, room, []string{"hs1"})

	// Charlie should join the space (which gives access to the room).
	charlie := deployment.Client(t, "hs3", "@charlie:hs3")
	charlie.JoinRoom(t, space, []string{"hs1"})

	// hs2 doesn't have anyone to invite from, so the join fails.
	failJoinRoom(t, charlie, room, "hs2", 502)

	// Including hs1 (and failing over to it) allows the join to succeed.
	charlie.JoinRoom(t, room, []string{"hs2", "hs1"})

	// Double check that the join was authorised via hs1.
	bob.SyncUntilTimelineHas(
		t,
		room,
		func(ev gjson.Result) bool {
			if ev.Get("type").Str != "m.room.member" || ev.Get("state_key").Str != charlie.UserID {
				return false
			}
			must.EqualStr(t, ev.Get("content").Get("membership").Str, "join", "Charlie failed to join the room")
			must.EqualStr(t, ev.Get("content").Get("join_authorised_via_users_server").Str, alice.UserID, "Join authorised via incorrect server")

			return true
		},
	)

	// Bump the power-level of bob.
	alice.SendEventSynced(t, room, b.Event{
		Type:     "m.room.power_levels",
		StateKey: &state_key,
		Content: map[string]interface{}{
			"invite": 100,
			"users": map[string]interface{}{
				alice.UserID: 100,
				bob.UserID:   100,
			},
		},
	})

	// Charlie leaves the room (so they can rejoin).
	charlie.LeaveRoom(t, room)

	// Ensure the events have synced to hs2.
	bob.SyncUntilTimelineHas(
		t,
		room,
		func(ev gjson.Result) bool {
			if ev.Get("type").Str != "m.room.member" || ev.Get("state_key").Str != charlie.UserID {
				return false
			}
			return ev.Get("content").Get("membership").Str == "leave"
		},
	)

	// Bob leaves the space so that hs2 doesn't know if Charlie is in the space or not.
	bob.LeaveRoom(t, space)

	// hs2 cannot complete the join since they do not know if Charlie meets the
	// requirements (since it is no longer in the space).
	failJoinRoom(t, charlie, room, "hs2", 502)

	// Including hs1 (and failing over to it) allows the join to succeed.
	charlie.JoinRoom(t, room, []string{"hs2", "hs1"})

	// Double check that the join was authorised via hs1.
	bob.SyncUntilTimelineHas(
		t,
		room,
		func(ev gjson.Result) bool {
			if ev.Get("type").Str != "m.room.member" || ev.Get("state_key").Str != charlie.UserID {
				return false
			}
			must.EqualStr(t, ev.Get("content").Get("membership").Str, "join", "Charlie failed to join the room")
			must.EqualStr(t, ev.Get("content").Get("join_authorised_via_users_server").Str, alice.UserID, "Join authorised via incorrect server")

			return true
		},
	)
}

// Request the room summary and ensure the expected rooms are in the response.
func requestAndAssertSummary(t *testing.T, user *client.CSAPI, space string, expected_rooms []interface{}) {
	t.Helper()

	res := user.MustDo(t, "POST", []string{"_matrix", "client", "unstable", "org.matrix.msc2946", "rooms", space, "spaces"}, map[string]interface{}{})
	must.MatchResponse(t, res, match.HTTPResponse{
		JSON: []match.JSON{
			match.JSONCheckOff("rooms", expected_rooms, func(r gjson.Result) interface{} {
				return r.Get("room_id").Str
			}, nil),
		},
	})
}

// Tests that MSC2946 works for a restricted room.
//
// Create a space with a room in it that has join rules restricted to membership
// in that space.
//
// The user should be unable to see the room in the spaces summary unless they
// are a member of the space.
func TestRestrictedRoomsSpacesSummary(t *testing.T) {
	deployment := Deploy(t, b.BlueprintOneToOneRoom)
	defer deployment.Destroy(t)

	// Create the rooms
	alice := deployment.Client(t, "hs1", "@alice:hs1")
	space := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"name":   "Space",
		"creation_content": map[string]interface{}{
			"type": "m.space",
		},
		// World readable to allow peeking without joining.
		"initial_state": []map[string]interface{}{
			{
				"type":      "m.room.history_visibility",
				"state_key": "",
				"content": map[string]interface{}{
					"history_visibility": "world_readable",
				},
			},
		},
	})
	// The room is an unstable room version which supports the restricted join_rule.
	room := alice.CreateRoom(t, map[string]interface{}{
		"preset":       "public_chat",
		"name":         "Room",
		"room_version": "8",
		"initial_state": []map[string]interface{}{
			{
				"type":      "m.room.join_rules",
				"state_key": "",
				"content": map[string]interface{}{
					"join_rule": "restricted",
					"allow": []map[string]interface{}{
						{
							"type":    "m.room_membership",
							"room_id": &space,
							"via":     []string{"hs1"},
						},
					},
				},
			},
		},
	})
	alice.SendEventSynced(t, space, b.Event{
		Type:     "m.space.child",
		StateKey: &room,
		Content: map[string]interface{}{
			"via": []string{"hs1"},
		},
	})

	t.Logf("Space: %s", space)
	t.Logf("Room: %s", room)

	// Create a second user on the same homeserver.
	bob := deployment.Client(t, "hs1", "@bob:hs1")

	// Querying the space returns only the space, as the room is restricted.
	requestAndAssertSummary(t, bob, space, []interface{}{space})

	// Join the space, and now the restricted room should appear.
	bob.JoinRoom(t, space, []string{"hs1"})
	requestAndAssertSummary(t, bob, space, []interface{}{space, room})
}

// Tests that MSC2946 works over federation for a restricted room.
//
// Create a space with a room in it that has join rules restricted to membership
// in that space. The space and room are on different homeservers. While generating
// the summary of space hs1 needs to ask hs2 to generate the summary for room since
// it is not participating in the room.
//
// The user should be unable to see the room in the spaces summary unless they
// are a member of the space.
//
// This tests the interactions over federation where the space and room are on
// different homeservers, and one might not have the proper information needed to
// decide if a user is in a room.
func TestRestrictedRoomsSpacesSummaryFederation(t *testing.T) {
	deployment := Deploy(t, b.BlueprintFederationTwoLocalOneRemote)
	defer deployment.Destroy(t)

	// Create the rooms
	alice := deployment.Client(t, "hs1", "@alice:hs1")
	bob := deployment.Client(t, "hs1", "@bob:hs1")
	space := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
		"name":   "Space",
		"creation_content": map[string]interface{}{
			"type": "m.space",
		},
		"initial_state": []map[string]interface{}{
			{
				"type":      "m.room.history_visibility",
				"state_key": "",
				"content": map[string]string{
					"history_visibility": "world_readable",
				},
			},
		},
	})

	// The room is an unstable room version which supports the restricted join_rule
	// and is created on hs2.
	charlie := deployment.Client(t, "hs2", "@charlie:hs2")
	room := charlie.CreateRoom(t, map[string]interface{}{
		"preset":       "public_chat",
		"name":         "Room",
		"room_version": "8",
		"initial_state": []map[string]interface{}{
			{
				"type":      "m.room.join_rules",
				"state_key": "",
				"content": map[string]interface{}{
					"join_rule": "restricted",
					"allow": []map[string]interface{}{
						{
							"type":    "m.room_membership",
							"room_id": &space,
							"via":     []string{"hs1"},
						},
					},
				},
			},
		},
	})

	// create the link (this doesn't really make sense since how would alice know
	// about the room? but it works for testing)
	alice.SendEventSynced(t, space, b.Event{
		Type:     spaceChildEventType,
		StateKey: &room,
		Content: map[string]interface{}{
			"via": []string{"hs2"},
		},
	})

	// The room appears for neither alice or bob initially. Although alice is in
	// the space and should be able to access the room, hs2 doesn't know this!
	requestAndAssertSummary(t, alice, space, []interface{}{space})
	requestAndAssertSummary(t, bob, space, []interface{}{space})

	// charlie joins the space and now hs2 knows that alice is in the space (and
	// can join the room).
	charlie.JoinRoom(t, space, []string{"hs1"})

	// The restricted room should appear for alice (who is in the space).
	requestAndAssertSummary(t, alice, space, []interface{}{space, room})
	requestAndAssertSummary(t, bob, space, []interface{}{space})
}
