"""Run a real recipe of the justfile in a throwaway git repository, as the recipes' tests do."""
import shutil
import subprocess
import tempfile
from contextlib import contextmanager
from pathlib import Path

JUSTFILE = Path(__file__).resolve().parents[1] / "justfile"
GIT = ["git", "-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "core.autocrlf=false"]


def git(repo, *args):
    subprocess.run([*GIT, "-C", str(repo), *args], check=True, stdout=subprocess.DEVNULL)


def write(repo, files):
    """Write files ({path: content}) into the repository; a content of None deletes the file."""
    for path, content in files.items():
        target = Path(repo) / path
        if content is None:
            target.unlink()
            continue
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content, encoding="utf-8", newline="\n")


def commit(repo, files):
    write(repo, files)
    git(repo, "add", ".")
    git(repo, "commit", "--quiet", "-m", "plant")


@contextmanager
def repository():
    with tempfile.TemporaryDirectory() as repo:
        subprocess.run([*GIT, "init", "--quiet", repo], check=True)
        yield repo


def just(repo, *args):
    return subprocess.run([shutil.which("just"), "--justfile", str(JUSTFILE), "--working-directory", str(repo), *args],
                          stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, encoding="utf-8")


def just_with_broken_git(*args):
    """Run a recipe where every git command fails: the directory has a .git that points nowhere."""
    with tempfile.TemporaryDirectory() as repo:
        (Path(repo) / ".git").write_text("gitdir: nowhere\n")
        return just(repo, *args)
