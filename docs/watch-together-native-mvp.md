# Watch Together: Native MVP

## Scope

Watch Together is native-app only. A host creates a room for a specific title, explicitly invites profiles from their own account or another account on the same MediaStorm server, and starts synchronized playback when desired. Readiness is informational and never blocks playback.

Web and external-player playback are not part of this first version.

## User experience

1. The host opens **Details → More Options → Watch Together**, selects household profiles, and/or enters the exact username of another account on the server.
2. The new room opens in a lobby. Each participant is shown as:
   - invited but not joined;
   - joined but away; or
   - currently present.
3. Hosts and guests can independently mark themselves ready. The host may start without waiting for readiness.
4. Profile invitees receive a persistent Watch Together shelf in the first home position. Account invitees see the same shelf and explicitly accept or decline using their currently selected profile.
5. Starting the room keeps a room-style preparation view visible while the app resolves playback.
6. During playback, the controls overlay shows room presence below the top-right media information. It is hidden whenever the controls are hidden.

Any joined participant can currently play, pause, or seek for the room. The latest accepted state update wins, and other native clients normally converge within approximately one second.

## Architecture

### Backend

Migration `046_watch_rooms.sql` adds three PostgreSQL tables:

- `watch_rooms`: media identity, details-route parameters, playback state, revision, expiry, and creator.
- `watch_room_invites`: the profiles allowed to discover and join a room.
- `watch_room_members`: joined clients, readiness, buffering, and heartbeat timestamps.

Migration `048_watch_room_account_invites.sql` adds pending same-server account invitations. These address an exact account username without revealing its profile directory. Acceptance atomically binds a profile owned by the recipient account and inserts the existing profile invitation used by all room operations.

The watch-room service validates profile access, calculates presence from a 15-second heartbeat window, advances the effective position while a room is playing, and restricts room termination to its creator. Rooms currently expire after 24 hours.

The profile-scoped API supports:

| Operation | Purpose |
| --- | --- |
| Create | Create the room and its explicit invitation list. |
| Invitations | Populate the persistent native home shelf. |
| Account invite | Invite an exact same-server account, list pending invitations, accept/decline as an owned profile, or revoke as host. |
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

- Cross-server invitation and synchronization are not implemented.
- Synchronization uses polling and is deliberately best-effort; simultaneous commands are last-write-wins.
- There is no explicit server-side playback-leader role.
- Host disconnect and all-away detection use a two-minute grace period rather than a transport-level disconnect signal.
- Ended room records are retained for 24 hours before cleanup; this is an operational audit window, not permanent history.

## Next steps

### Same-server account invitations (implemented)

The host can enter an exact account username; there is no searchable account or profile directory. The server stores a pending account-level invitation tied to the room expiry. It appears on the recipient account's Watch Together shelf regardless of the active profile, but no recipient profile is added to the room roster before consent.

Accepting verifies that the selected profile belongs to the authenticated recipient account, locks the pending invitation, inserts the profile-scoped room invitation, and consumes the account invitation in one transaction. Replays and expired, declined, revoked, or ended-room invitations are rejected. The host sees pending account usernames in the lobby and can revoke them. Direct profile IDs are restricted to the creator's account, closing the previous request-level bypass around the local profile picker.

All subsequent discovery, membership, presence, readiness, synchronization, and teardown use the existing profile-scoped API. Both accounts resolve and stream the title through their own profile settings and access policies on the shared server.

### Cross-server invitations

The Iroh invitation path remains the proposed bootstrap for accounts on different MediaStorm servers. The host server would remain authoritative for room membership, synchronization, and teardown, while a short-lived one-time claim secret and room-scoped credential provide narrowly scoped remote access. The accepted external principal should fit behind the same invitation/acceptance abstraction now used for same-server accounts, without sharing ordinary sessions, profile directories, stream URLs, or provider credentials.

### Enforce the native player (implemented)

Room playback now carries explicit `watchRoomPlayback` and `watchRoomId` route/session flags. Playback selection uses the flag to bypass external-player preferences and the player picker, ignores Cast/DLNA launch intents, hides in-player Cast/DLNA actions, and selects the synchronization-capable native implementation. Synchronization activates only for an explicitly flagged room session instead of any playback that happens to match a persisted room.

Clients now report `nativePlayback`, `stateSync`, and protocol version when creating or joining a room. The backend rejects clients without both synchronization capabilities or protocol version 1 support, and the accepted capability set is retained with each member.

### Allow rejoining after player exit (implemented)

Exiting playback is distinct from leaving the room. A normal Back/Exit replaces the player with the lobby, retains membership and the active-room reference, suppresses the guest auto-open behavior for that return, and offers **Resume room** while the room remains active. **Leave room** remains an explicit action that removes guest membership, while the host has an explicit **End room** action.

On application launch or profile activation, the client validates the persisted room, profile, and media identity against the server and restores the lobby. Ended rooms, mismatched media/profile state, and authorization/not-found responses clear the stale local reference. Temporary connectivity failures preserve it for a later retry.

### Deterministic room teardown (implemented)

Teardown semantics are defined for four cases:

1. **Host ends room:** immediately and idempotently mark it ended with `host_ended`; polling clients receive the terminal room, stop synchronization, clear local state, and return to the lobby.
2. **Host disconnects:** retain the room for a two-minute grace period, then end it with `host_disconnected` when another participant remains active.
3. **All participants leave or go stale:** end after the same grace period with `all_left`.
4. **Expiry:** mark the room ended with `expired`, retain the terminal response for 24 hours, and then delete it transactionally through cascading room data cleanup.

Teardown records an end reason and timestamp, rejects subsequent join/state mutations, and returns a terminal response during the audit window. The cleanup worker logs ended/deleted counts; richer active-room and synchronization metrics can be added when the project has a metrics exporter.

## Verification status

Backend tests cover room creation, same-account profile enforcement, cross-account invitation acceptance and replay rejection, invitation filtering, capability rejection, invited-versus-joined membership, joining, readiness/state authorization, playback-position advancement, terminal mutation rejection, and stale-room cleanup. Frontend tests cover explicit native-playback enforcement plus persisted-session storage and legacy migration. Same-server multi-account UX, native multi-device playback, device-level external-player suppression, lobby focus behavior, re-entry, background restoration, and disconnect teardown remain manual acceptance work.
