# fake slirp+sshd on 127.0.0.1:2222. mode stall: before T, connections are
# accepted and never answered (guest has no address); close: accepted and
# closed at once (guest RST); refuse: no listener before T. After T: banner.
import socket, sys, time
mode, T = sys.argv[1], float(sys.argv[2])
t0 = time.time()
held = []
if mode == "refuse":
    time.sleep(T)
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 2222)); s.listen(64)
while True:
    c, _ = s.accept()
    if time.time() - t0 < T:
        if mode == "close": c.close()
        else: held.append(c)
    elif mode == "slow":  # a banner 1s after every accept
        import threading
        threading.Timer(1.0, c.sendall, [b"SSH-2.0-OpenSSH_fake\r\n"]).start(); held.append(c)
    else:
        c.sendall(b"SSH-2.0-OpenSSH_fake\r\n"); held.append(c)
