#!/usr/bin/env python3
"""Manage the MediaStorm Vikunja task lifecycle with compact, safe commands."""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


PROJECT_IDENTIFIER = "MEDIASTORM"
DEFAULT_BASE_URL = "http://127.0.0.1:8082/api/v2"
CREATE_STATES = ("Backlog", "Claimed", "In Progress")


class WorkflowError(RuntimeError):
    """A concise, user-actionable workflow failure."""


def load_env_file(path: Path) -> None:
    """Load simple KEY=VALUE entries without evaluating shell syntax."""
    if not path.exists():
        return
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip()
        if value[:1] == value[-1:] and value[:1] in {"'", '"'}:
            value = value[1:-1]
        if key:
            os.environ.setdefault(key, value)


class VikunjaClient:
    def __init__(self, base_url: str, token: str, timeout: float = 30):
        root = base_url.rstrip("/")
        self.base_url = root if root.endswith("/api/v2") else root + "/api/v2"
        self.token = token
        self.timeout = timeout

    def request(self, method: str, path: str, body: dict[str, Any] | None = None) -> dict[str, Any]:
        data = None if body is None else json.dumps(body).encode("utf-8")
        request = urllib.request.Request(
            self.base_url + path,
            data=data,
            method=method,
            headers={
                "Authorization": f"Bearer {self.token}",
                "Content-Type": "application/json",
                "User-Agent": "mediastorm-vikunja-task/1",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                payload = response.read()
                return json.loads(payload) if payload else {}
        except urllib.error.HTTPError as error:
            detail = error.read().decode("utf-8", errors="replace")
            raise WorkflowError(f"Vikunja {method} {path} failed ({error.code}): {detail}") from error
        except urllib.error.URLError as error:
            raise WorkflowError(f"Cannot reach Vikunja at {self.base_url}: {error.reason}") from error

    def paginated(self, path: str) -> list[dict[str, Any]]:
        separator = "&" if "?" in path else "?"
        response = self.request("GET", f"{path}{separator}page=1&per_page=1000")
        items = response.get("items")
        if not isinstance(items, list):
            raise WorkflowError(f"Vikunja returned an invalid paginated response for {path}")
        return items


class VikunjaWorkflow:
    def __init__(self, client: VikunjaClient, project_identifier: str = PROJECT_IDENTIFIER):
        self.client = client
        self.project_identifier = project_identifier
        self._project: dict[str, Any] | None = None
        self._view: dict[str, Any] | None = None
        self._buckets: dict[str, dict[str, Any]] | None = None

    def project(self) -> dict[str, Any]:
        if self._project is None:
            matches = [
                project
                for project in self.client.paginated("/projects")
                if project.get("identifier") == self.project_identifier
            ]
            if len(matches) != 1:
                raise WorkflowError(
                    f"Expected one project with identifier {self.project_identifier!r}; found {len(matches)}"
                )
            self._project = matches[0]
        return self._project

    def kanban_view(self) -> dict[str, Any]:
        if self._view is None:
            project = self.project()
            views = project.get("views")
            if not isinstance(views, list):
                views = self.client.paginated(f"/projects/{project['id']}/views")
            matches = [view for view in views if view.get("view_kind") == "kanban"]
            if len(matches) != 1:
                raise WorkflowError(f"Expected one Kanban view for {self.project_identifier}; found {len(matches)}")
            self._view = matches[0]
        return self._view

    def buckets(self) -> dict[str, dict[str, Any]]:
        if self._buckets is None:
            project = self.project()
            view = self.kanban_view()
            self._buckets = {
                bucket["title"]: bucket
                for bucket in self.client.paginated(f"/projects/{project['id']}/views/{view['id']}/buckets")
                if bucket.get("title")
            }
        return self._buckets

    def resolve_task(self, reference: str) -> dict[str, Any]:
        normalized = reference.strip().upper()
        numeric_id = normalized
        if normalized.startswith(self.project_identifier + "-"):
            numeric_id = normalized.removeprefix(self.project_identifier + "-")
        if numeric_id.isdigit():
            task = self.client.request("GET", f"/tasks/{numeric_id}")
            identifier = str(task.get("identifier") or "").upper()
            if normalized.startswith(self.project_identifier + "-") and identifier != normalized:
                raise WorkflowError(f"Task {numeric_id} is {identifier or 'unidentified'}, not {normalized}")
            return task

        encoded = urllib.parse.quote(reference.strip())
        matches = [
            task
            for task in self.client.paginated(f"/projects/{self.project()['id']}/tasks?s={encoded}")
            if str(task.get("identifier") or "").upper() == normalized
        ]
        if len(matches) != 1:
            raise WorkflowError(f"Expected one task matching {reference!r}; found {len(matches)}")
        return matches[0]

    def current_user(self) -> dict[str, Any]:
        return self.client.request("GET", "/user")

    def find_user(self, username: str | None) -> dict[str, Any]:
        current = self.current_user()
        if not username or current.get("username") == username:
            return current
        encoded = urllib.parse.quote(username)
        matches = [user for user in self.client.paginated(f"/users?s={encoded}") if user.get("username") == username]
        if len(matches) != 1:
            raise WorkflowError(f"Expected one Vikunja user named {username!r}; found {len(matches)}")
        return matches[0]

    def assign(self, task: dict[str, Any], user: dict[str, Any]) -> dict[str, Any]:
        assignees = task.get("assignees") or []
        assigned_ids = {assignee.get("id") for assignee in assignees}
        if assigned_ids and user.get("id") not in assigned_ids:
            owners = ", ".join(str(assignee.get("username") or assignee.get("id")) for assignee in assignees)
            raise WorkflowError(f"{task_reference(task)} is already assigned to {owners}")
        if user.get("id") not in assigned_ids:
            self.client.request("POST", f"/tasks/{task['id']}/assignees", {"user_id": user["id"]})
        refreshed = self.client.request("GET", f"/tasks/{task['id']}")
        if user.get("id") not in {assignee.get("id") for assignee in refreshed.get("assignees") or []}:
            raise WorkflowError(f"Vikunja did not confirm assignment of {task_reference(task)}")
        return refreshed

    def move(self, task: dict[str, Any], state: str) -> None:
        bucket = self.buckets().get(state)
        if bucket is None:
            raise WorkflowError(f"The {state!r} bucket does not exist in {self.project_identifier}")
        project = self.project()
        view = self.kanban_view()
        self.client.request(
            "PUT",
            f"/projects/{project['id']}/views/{view['id']}/buckets/{bucket['id']}/tasks",
            {"task_id": task["id"]},
        )

    def add_comment(self, task: dict[str, Any], comment: str) -> None:
        self.client.request("POST", f"/tasks/{task['id']}/comments", {"comment": comment.strip()})

    def create(self, title: str, description: str, state: str, comment: str, unassigned: bool) -> dict[str, Any]:
        if unassigned and state != "Backlog":
            raise WorkflowError("Only Backlog tasks may be created unassigned")
        task = self.client.request(
            "POST",
            f"/projects/{self.project()['id']}/tasks",
            {"title": title.strip(), "description": description.strip()},
        )
        if not unassigned:
            task = self.assign(task, self.current_user())
        self.move(task, state)
        self.add_comment(task, comment)
        return task

    def claim(self, reference: str, state: str, comment: str, username: str | None = None) -> dict[str, Any]:
        task = self.resolve_task(reference)
        task = self.assign(task, self.find_user(username))
        self.move(task, state)
        self.add_comment(task, comment)
        return task

    def transition(self, reference: str, state: str, comment: str) -> dict[str, Any]:
        task = self.resolve_task(reference)
        current = self.current_user()
        if current.get("id") not in {assignee.get("id") for assignee in task.get("assignees") or []}:
            raise WorkflowError(f"{task_reference(task)} is not assigned to the authenticated user")
        self.move(task, state)
        self.add_comment(task, comment)
        return task


def task_reference(task: dict[str, Any]) -> str:
    return str(task.get("identifier") or task.get("id") or "unknown-task")


def format_result(task: dict[str, Any], state: str | None = None) -> str:
    owners = ",".join(str(owner.get("username") or owner.get("id")) for owner in task.get("assignees") or [])
    fields = [task_reference(task)]
    if state:
        fields.append(state)
    fields.extend([owners or "unassigned", str(task.get("title") or "")])
    return " | ".join(fields)


def add_task_argument(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("task", help="Numeric task ID or identifier such as MEDIASTORM-60")


def build_parser() -> argparse.ArgumentParser:
    repo_root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, default=repo_root / "docs" / ".env")
    parser.add_argument("--base-url", help="Vikunja root URL or API v2 URL")
    parser.add_argument("--project", default=PROJECT_IDENTIFIER, help="Project identifier")
    subparsers = parser.add_subparsers(dest="command", required=True)

    status = subparsers.add_parser("status", help="Show compact task ownership and title")
    add_task_argument(status)

    create = subparsers.add_parser("create", help="Create, optionally assign, move, and comment in one invocation")
    create.add_argument("title")
    create.add_argument("--description", default="")
    create.add_argument("--state", choices=CREATE_STATES, default="Claimed")
    create.add_argument("--comment", required=True)
    create.add_argument("--unassigned", action="store_true", help="Create an unassigned Backlog task")

    claim = subparsers.add_parser("claim", help="Verify availability, assign, move, and comment")
    add_task_argument(claim)
    claim.add_argument("--state", choices=("Claimed", "In Progress"), default="Claimed")
    claim.add_argument("--assignee", help="Defaults to the authenticated user")
    claim.add_argument("--comment", required=True)

    comment = subparsers.add_parser("comment", help="Record one material progress note")
    add_task_argument(comment)
    comment.add_argument("--comment", required=True)

    for command, state, help_text in (
        ("testing", "Testing", "Move an owned task to Testing with explicit test steps"),
        ("in-progress", "In Progress", "Reopen an owned task with a reason"),
    ):
        transition = subparsers.add_parser(command, help=help_text)
        add_task_argument(transition)
        transition.add_argument("--comment", required=True)
        transition.set_defaults(target_state=state)

    done = subparsers.add_parser("done", help="Close an owned task after user confirmation")
    add_task_argument(done)
    done.add_argument("--verification", required=True, help="Who confirmed the result and how")
    done.add_argument("--commit", help="Final commit hash")
    done.add_argument("--comment", help="Additional closing context")
    done.set_defaults(target_state="Done")

    return parser


def run(args: argparse.Namespace, client: VikunjaClient) -> str:
    workflow = VikunjaWorkflow(client, args.project)
    if args.command == "status":
        return format_result(workflow.resolve_task(args.task))
    if args.command == "create":
        task = workflow.create(args.title, args.description, args.state, args.comment, args.unassigned)
        return format_result(task, args.state)
    if args.command == "claim":
        task = workflow.claim(args.task, args.state, args.comment, args.assignee)
        return format_result(task, args.state)
    if args.command == "comment":
        task = workflow.resolve_task(args.task)
        workflow.add_comment(task, args.comment)
        return format_result(task)
    if args.command == "done":
        parts = [args.verification.strip()]
        if args.commit:
            parts.append(f"Commit: {args.commit.strip()}.")
        if args.comment:
            parts.append(args.comment.strip())
        closing_comment = " ".join(part for part in parts if part)
        task = workflow.transition(args.task, args.target_state, closing_comment)
        return format_result(task, args.target_state)
    if args.command in {"testing", "in-progress"}:
        task = workflow.transition(args.task, args.target_state, args.comment)
        return format_result(task, args.target_state)
    raise WorkflowError(f"Unsupported command: {args.command}")


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    load_env_file(args.env_file)
    token = os.environ.get("VIKUNJA_API_TOKEN", "").strip()
    if not token:
        parser.error(f"VIKUNJA_API_TOKEN is not set and was not found in {args.env_file}")
    base_url = args.base_url or os.environ.get("VIKUNJA_BASE_URL") or DEFAULT_BASE_URL
    try:
        print(run(args, VikunjaClient(base_url, token)))
    except WorkflowError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
