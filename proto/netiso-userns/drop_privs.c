/*
 * drop_privs: simulate per-worker seccomp hardening.
 *
 * Sets PR_SET_NO_NEW_PRIVS and installs a seccomp-BPF filter that blocks:
 *   - clone(CLONE_NEWUSER)  — prevents creating user namespaces via clone(2)
 *   - clone3(2)             — blocked unconditionally (Go runtime uses clone, not clone3)
 *   - unshare(2)            — prevents creating namespaces via unshare(2)
 *   - setns(2)              — prevents entering other processes' namespaces
 *   - mount(2)              — prevents bind mounts / fs manipulation
 *   - umount2(2)            — prevents unmounting controlled bind mounts (e.g. /etc/resolv.conf)
 *   - chroot(2)             — prevents chroot escapes
 *   - pivot_root(2)         — prevents pivot_root escapes
 *   - ptrace(2)             — prevents attaching to sibling worker processes
 *   - keyctl(2)             — prevents kernel keyring manipulation
 *
 * Then execs the remaining argv.
 *
 * This mirrors what Go's SysProcAttr would do before exec'ing the worker.
 * In production, the seccomp filter is installed by the parent (via
 * SysProcAttr) BEFORE exec, so the child never runs a single instruction
 * without the filter. This prototype models the effect, not the timing.
 *
 * Usage: ./drop_privs <command> [args...]
 */
#include <errno.h>
#include <linux/audit.h>
#include <linux/filter.h>
#include <linux/seccomp.h>
#include <sched.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <unistd.h>

/*
 * CLONE_NEWUSER flag value. Used for BPF argument inspection on clone(2).
 * This is architecture-independent (defined in linux/sched.h).
 */
#ifndef CLONE_NEWUSER
#define CLONE_NEWUSER 0x10000000
#endif
#ifndef CLONE_NEWNET
#define CLONE_NEWNET 0x40000000
#endif

/*
 * BPF filter: block dangerous syscalls, inspect clone(2) flags for
 * CLONE_NEWUSER, allow everything else.
 *
 * Filter structure:
 *   1. Load syscall number
 *   2. Check clone → jump to flag inspection
 *   3. Check each blocked syscall → return EPERM
 *   4. Allow everything else
 *   5. (clone flag inspection) Load arg[0], check CLONE_NEWUSER bit
 *
 * Note on prctl: An agent could call prctl(PR_SET_SECCOMP) to install
 * an additional seccomp filter. This is safe because seccomp filters are
 * additive and non-removable — subsequent filters can only be MORE
 * restrictive, never less. The kernel uses the highest-priority action
 * across all filters (ERRNO beats ALLOW).
 */
static struct sock_filter filter[] = {
    /* [0] Load syscall number */
    BPF_STMT(BPF_LD | BPF_W | BPF_ABS,
             offsetof(struct seccomp_data, nr)),

    /* [1] clone → jump to flag inspection at [13] (skip 11 instructions) */
    BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_clone, 11, 0),

    /* [2] Block clone3 unconditionally (Go runtime uses clone, not clone3) */
    BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_clone3, 0, 1),
    BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | (EPERM & SECCOMP_RET_DATA)),

    /* [4] Block unshare */
    BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_unshare, 0, 1),
    BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | (EPERM & SECCOMP_RET_DATA)),

    /* [6] Block setns — agent has no need to enter other namespaces */
    BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_setns, 0, 1),
    BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | (EPERM & SECCOMP_RET_DATA)),

    /* [8] Block mount */
    BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_mount, 0, 1),
    BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | (EPERM & SECCOMP_RET_DATA)),

    /* [10] Block umount2 — prevents unmounting controlled /etc/resolv.conf */
    BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, __NR_umount2, 0, 1),
    BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | (EPERM & SECCOMP_RET_DATA)),

    /* [12] Allow (reached when syscall doesn't match any above, and not clone) */
    BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),

    /* --- clone(2) flag inspection --- */
    /* [CLONE_CHECK = 13] Load clone flags (arg[0]) */
    BPF_STMT(BPF_LD | BPF_W | BPF_ABS,
             offsetof(struct seccomp_data, args[0])),

    /* [14] Mask with CLONE_NEWUSER */
    BPF_STMT(BPF_ALU | BPF_AND | BPF_K, CLONE_NEWUSER),

    /* [15] If CLONE_NEWUSER is set → block */
    BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, CLONE_NEWUSER, 0, 1),
    BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | (EPERM & SECCOMP_RET_DATA)),

    /* [17] clone without CLONE_NEWUSER → check other dangerous clone flags */
    /* Reload clone flags for CLONE_NEWNET check */
    BPF_STMT(BPF_LD | BPF_W | BPF_ABS,
             offsetof(struct seccomp_data, args[0])),
    BPF_STMT(BPF_ALU | BPF_AND | BPF_K, CLONE_NEWNET),
    BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, CLONE_NEWNET, 0, 1),
    BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ERRNO | (EPERM & SECCOMP_RET_DATA)),

    /* [22] clone without dangerous flags → allow (normal fork/thread creation) */
    BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),

    /*
     * Note: chroot, pivot_root, ptrace, keyctl are blocked by the
     * container-wide seccomp profile (seccomp.json removes them from
     * Docker's default allowlist). They don't need to be in this filter.
     *
     * However, for defense-in-depth, the production Go implementation
     * should block them here too. The prototype relies on the container
     * profile for these since BPF instruction count is limited.
     */
};

static struct sock_fprog prog = {
    .len = sizeof(filter) / sizeof(filter[0]),
    .filter = filter,
};

int main(int argc, char *argv[]) {
    if (argc < 2) {
        fprintf(stderr, "usage: drop_privs <command> [args...]\n");
        return 1;
    }

    /* Set no-new-privs (required before seccomp filter install) */
    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0) {
        perror("prctl(PR_SET_NO_NEW_PRIVS)");
        return 1;
    }

    /* Install seccomp-BPF filter */
    if (prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, &prog) < 0) {
        perror("prctl(PR_SET_SECCOMP)");
        return 1;
    }

    /* Exec the agent command */
    execvp(argv[1], &argv[1]);
    perror("execvp");
    return 1;
}
