FROM ubuntu:24.04
RUN apt-get update && apt-get install -y \
    iproute2 nftables iputils-ping util-linux mount gcc libc6-dev uidmap \
    && rm -rf /var/lib/apt/lists/*
COPY test.sh seccomp.json drop_privs.c try_clone_newuser.c try_setns.c /proto/
WORKDIR /proto
RUN chmod +x test.sh \
    && gcc -static -o drop_privs drop_privs.c \
    && gcc -static -o try_clone_newuser try_clone_newuser.c \
    && gcc -static -o try_setns try_setns.c
CMD ["bash", "test.sh"]
