/*
 * try_clone_newuser: attempt clone(CLONE_NEWUSER) via the clone(2) syscall.
 *
 * Used to verify that the per-worker seccomp filter blocks clone(2) with
 * CLONE_NEWUSER in the flags. The shell 'unshare' command uses the
 * unshare(2) syscall, which is a different code path.
 *
 * Exit codes:
 *   0 — clone(CLONE_NEWUSER) succeeded (BAD — seccomp should block this)
 *   1 — clone(CLONE_NEWUSER) failed with EPERM (GOOD — seccomp blocked it)
 *   2 — clone(CLONE_NEWUSER) failed with other error
 */
#define _GNU_SOURCE
#include <errno.h>
#include <sched.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

#define STACK_SIZE (64 * 1024)

static int child_fn(void *arg) {
    (void)arg;
    _exit(0);
}

int main(void) {
    char *stack = malloc(STACK_SIZE);
    if (!stack) {
        perror("malloc");
        return 2;
    }

    int pid = clone(child_fn, stack + STACK_SIZE,
                    CLONE_NEWUSER | SIGCHLD, NULL);

    if (pid >= 0) {
        waitpid(pid, NULL, 0);
        fprintf(stderr, "clone(CLONE_NEWUSER) succeeded — NOT blocked!\n");
        free(stack);
        return 0;
    }

    if (errno == EPERM) {
        fprintf(stderr, "clone(CLONE_NEWUSER) blocked by seccomp (EPERM)\n");
        free(stack);
        return 1;
    }

    fprintf(stderr, "clone(CLONE_NEWUSER) failed: %s (errno=%d)\n",
            strerror(errno), errno);
    free(stack);
    return 2;
}
