# Watch Together: Native MVP

## Scope

The first Watch Together implementation is native-app only. A host creates a room for a specific title, explicitly invites profiles associated with the same MediaStorm account, and starts synchronized playback when desired. Readiness is informational and never blocks playback.

Web and external-player playback are not part of this first version.

## User experience

1. The host opens **Details → More Options → Watch Together** and selects one or more profiles.
2. The new room opens in a lobby. Each participant is shown as:
   - invited but not joined;
   - joined but away; or
   - currently present.
3. Hosts and guests can independently mark themselves ready. The host may start without waiting for readiness.
4. Invitees receive a persistent Watch Together shelf in the first home position. The shelf is always enabled.
5. Starting the room keeps a room-style preparation view visible while the app resolves playback.
6. During playback, the controls overlay shows room presence below the top-right media information. It is hidden whenever the controls are hidden.

Any joined participant can currently play, pause, or seek for the room. The latest accepted state update wins, and other native clients normally converge within approximately one second.

## Architecture

### Backend

Migration `046_watch_rooms.sql` adds three PostgreSQL tables:

- `watch_rooms`: media identity, details-route parameters, playback state, revision, expiry, and creator.
- `watch_room_invites`: the profiles allowed to discover and join a room.
- `watch_room_members`: joined clients, readiness, buffering, and heartbeat timestamps.

The watch-room service validates profile access, calculates presence from a 15-second heartbeat window, advances the effective position while a room is playing, and restricts room termination to its creator. Rooms currently expire after 24 hours.

The profile-scoped API supports:

| Operation | Purpose |
| --- | --- |
| Create | Create the room and its explicit invitation list. |
| Invitations | Populate the persistent native home shelf. |
| Get / Join | Read the lobby and turn an invitation into membership. |
| Ready | Update the participant's optional readiness state. |
| State | Publish play, pause, and seek position changes. |
| Heartbeat | Maintain presence, client identity, and buffering state. |
| Leave / End | Remove a guest membership or end the room as host. |

### Native frontend

- Details owns room creation and invitee selection.
- The lobby polls once per second and sends presence heartbeats.
- The active room ID is persisted in AsyncStorage for playback handoff.
- Playback polls room state once per second. Local pause/play and significant seeks create a new server revision; remote revisions are applied to the local player.
- The preparation overlay keeps room context visible during stream resolution.
- The player presence panel is part of the controls overlay and shares a measured top-right column with media information.

## Current limitations

- Invitees must be profiles already associated with the host account.
- Synchronization uses polling and is deliberately best-effort; simultaneous commands are last-write-wins.
- There is no explicit server-side playback-leader role.
- The route does not yet define a complete room re-entry flow after leaving the player.
- Expired and ended room records do not yet have a dedicated cleanup worker.

## Next steps

### Invite MediaStorm users outside the host account

Invitations should target an account-level identity rather than only a local profile ID. This needs an authenticated user directory or exact-address/user-code lookup, a durable external invitation record, and an acceptance flow that binds the recipient's chosen profile to the room.

The room service should represent invitees as principals such as `(account_id, profile_id?)`, avoid exposing an account's full profile list to another account, and issue a short-lived, single-purpose invitation token for deep links or notification delivery. Authorization must be checked against the accepted invitation on every room request. Cross-server rooms would additionally need a shared relay or one authoritative host server reachable by every participant.

### Enforce the native player

Room playback should carry an explicit `watchRoomPlayback` route/session flag. Playback selection must use that flag to bypass external-player preferences and the player picker, reject unsupported cast/external handoffs, and select a synchronization-capable native implementation.

The client should report a small capability set when joining, for example `nativePlayback`, `stateSync`, and protocol version. The backend can then refuse incompatible joins or show them clearly in the lobby instead of allowing a client that cannot follow room state.

### Allow rejoining after player exit

Exiting playback should be distinct from leaving the room. A normal Back/Exit should return to the lobby, retain membership and the active-room reference, and offer **Resume room** while the room remains active. **Leave room** should remain an explicit action that removes membership.

On application launch or profile activation, the client should validate the persisted active room against the server and restore either the lobby or active playback prompt. If the room ended, expired, changed title, or the profile lost access, the client should clear the stale local reference.

### Deterministic room teardown

Define teardown semantics for four cases:

1. **Host ends room:** immediately mark it ended, notify polling clients, stop state updates, and clear active-room references.
2. **Host disconnects:** keep the room recoverable for a short grace period, then either elect no leader and pause or end automatically.
3. **All participants leave:** end after a configurable idle grace period rather than retaining an apparently active room.
4. **Expiry:** a scheduled cleanup job should delete expired/ended room data after a short audit window.

Teardown should be idempotent and transactional. The service should record an end reason and timestamp, reject subsequent join/state mutations, and return a terminal room response long enough for clients to cleanly dismiss their UI. Metrics for active rooms, stale members, sync errors, and teardown reasons will make the lifecycle operable.

## Verification status

Backend tests cover room creation, invitation filtering, invited-versus-joined membership, joining, readiness/state authorization, and playback-position advancement. The full Go test suite passes. Frontend TypeScript and focused lint checks pass; native multi-device playback, focus behavior, rejoining, and teardown remain manual acceptance work.
