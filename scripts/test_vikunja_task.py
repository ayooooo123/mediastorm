from __future__ import annotations

import argparse
import contextlib
import importlib.util
import io
import os
import tempfile
import unittest
from pathlib import Path
from typing import Any


MODULE_PATH = Path(__file__).with_name("vikunja-task.py")
SPEC = importlib.util.spec_from_file_location("vikunja_task", MODULE_PATH)
assert SPEC and SPEC.loader
vikunja_task = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(vikunja_task)


class FakeClient:
    def __init__(self) -> None:
        self.calls: list[tuple[str, str, dict[str, Any] | None]] = []
        self.assigned = False

    def paginated(self, path: str) -> list[dict[str, Any]]:
        self.calls.append(("PAGINATED", path, None))
        if path == "/projects":
            return [{"id": 2, "identifier": "MEDIASTORM", "views": [{"id": 12, "view_kind": "kanban"}]}]
        if path == "/projects/2/views/12/buckets":
            return [
                {"id": 7, "title": "Backlog"},
                {"id": 10, "title": "Claimed"},
                {"id": 8, "title": "In Progress"},
                {"id": 11, "title": "Testing"},
                {"id": 9, "title": "Done"},
            ]
        raise AssertionError(f"Unexpected paginated request: {path}")

    def request(self, method: str, path: str, body: dict[str, Any] | None = None) -> dict[str, Any]:
        self.calls.append((method, path, body))
        if method == "GET" and path == "/user":
            return {"id": 1, "username": "godver3"}
        if method == "GET" and path == "/tasks/60":
            assignees = [{"id": 1, "username": "godver3"}] if self.assigned else []
            return {"id": 60, "identifier": "MEDIASTORM-60", "title": "Workflow helper", "assignees": assignees}
        if method == "POST" and path == "/tasks/60/assignees":
            self.assigned = True
            return {}
        if method in {"POST", "PUT"}:
            return {}
        raise AssertionError(f"Unexpected request: {method} {path}")


class LoadEnvFileTests(unittest.TestCase):
    def test_loads_values_without_overwriting_environment(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            env_path = Path(directory) / ".env"
            env_path.write_text("VIKUNJA_API_TOKEN='from-file'\nVIKUNJA_BASE_URL=http://localhost\n", encoding="utf-8")
            original_token = os.environ.get("VIKUNJA_API_TOKEN")
            original_url = os.environ.get("VIKUNJA_BASE_URL")
            try:
                os.environ["VIKUNJA_API_TOKEN"] = "from-environment"
                os.environ.pop("VIKUNJA_BASE_URL", None)
                vikunja_task.load_env_file(env_path)
                self.assertEqual(os.environ["VIKUNJA_API_TOKEN"], "from-environment")
                self.assertEqual(os.environ["VIKUNJA_BASE_URL"], "http://localhost")
            finally:
                if original_token is None:
                    os.environ.pop("VIKUNJA_API_TOKEN", None)
                else:
                    os.environ["VIKUNJA_API_TOKEN"] = original_token
                if original_url is None:
                    os.environ.pop("VIKUNJA_BASE_URL", None)
                else:
                    os.environ["VIKUNJA_BASE_URL"] = original_url


class WorkflowTests(unittest.TestCase):
    def test_create_command_cannot_skip_directly_to_done(self) -> None:
        parser = vikunja_task.build_parser()

        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            parser.parse_args(["create", "Unsafe task", "--state", "Done", "--comment", "Skip workflow"])

    def test_claim_assigns_moves_comments_and_confirms_ownership(self) -> None:
        client = FakeClient()
        workflow = vikunja_task.VikunjaWorkflow(client)

        task = workflow.claim("MEDIASTORM-60", "In Progress", "Starting in root main.")

        self.assertEqual(task["assignees"][0]["username"], "godver3")
        self.assertIn(("POST", "/tasks/60/assignees", {"user_id": 1}), client.calls)
        self.assertIn(
            ("PUT", "/projects/2/views/12/buckets/8/tasks", {"task_id": 60}),
            client.calls,
        )
        self.assertIn(("POST", "/tasks/60/comments", {"comment": "Starting in root main."}), client.calls)

    def test_rejects_claim_owned_by_someone_else(self) -> None:
        client = FakeClient()
        workflow = vikunja_task.VikunjaWorkflow(client)
        task = {"id": 60, "identifier": "MEDIASTORM-60", "assignees": [{"id": 2, "username": "other"}]}

        with self.assertRaisesRegex(vikunja_task.WorkflowError, "already assigned to other"):
            workflow.assign(task, {"id": 1, "username": "godver3"})

    def test_testing_uses_post_comment_and_authoritative_move_response(self) -> None:
        client = FakeClient()
        client.assigned = True
        args = argparse.Namespace(
            command="testing",
            task="MEDIASTORM-60",
            target_state="Testing",
            comment="Run the helper integration test.",
            project="MEDIASTORM",
        )

        result = vikunja_task.run(args, client)

        self.assertIn("MEDIASTORM-60 | Testing | godver3", result)
        self.assertIn(
            ("PUT", "/projects/2/views/12/buckets/11/tasks", {"task_id": 60}),
            client.calls,
        )
        self.assertIn(
            ("POST", "/tasks/60/comments", {"comment": "Run the helper integration test."}),
            client.calls,
        )

    def test_done_builds_one_closing_comment(self) -> None:
        client = FakeClient()
        client.assigned = True
        args = argparse.Namespace(
            command="done",
            task="MEDIASTORM-60",
            target_state="Done",
            verification="User confirmed the workflow.",
            commit="abc123",
            comment="No follow-up required.",
            project="MEDIASTORM",
        )

        vikunja_task.run(args, client)

        self.assertIn(
            (
                "POST",
                "/tasks/60/comments",
                {"comment": "User confirmed the workflow. Commit: abc123. No follow-up required."},
            ),
            client.calls,
        )


if __name__ == "__main__":
    unittest.main()
