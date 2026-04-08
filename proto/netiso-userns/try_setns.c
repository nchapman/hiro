/*
 * try_setns: attempt setns(2) on own network namespace.
 *
 * Used to verify the per-worker seccomp filter blocks setns(2).
 * Opens /proc/self/ns/net (own netns) and tries setns — if blocked,
 * an agent cannot enter other agents' namespaces either.
 *
 * Exit codes:
 *   0 — setns succeeded (BAD — seccomp should block this)
 *   1 — setns failed with EPERM (GOOD — seccomp blocked it)
 *   2 — other error
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <sched.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

int main(void) {
    int fd = open("/proc/self/ns/net", O_RDONLY);
    if (fd < 0) {
        fprintf(stderr, "open(/proc/self/ns/net) failed: %s\n", strerror(errno));
        return 2;
    }

    int ret = setns(fd, CLONE_NEWNET);
    close(fd);

    if (ret == 0) {
        fprintf(stderr, "setns succeeded — NOT blocked!\n");
        return 0;
    }

    if (errno == EPERM) {
        fprintf(stderr, "setns blocked by seccomp (EPERM)\n");
        return 1;
    }

    fprintf(stderr, "setns failed: %s (errno=%d)\n", strerror(errno), errno);
    return 2;
}
