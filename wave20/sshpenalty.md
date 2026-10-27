# sshd PerSourcePenalties vs CI ssh polling (Ubuntu 26.10)

Sources (all fetched from archive.ubuntu.com, read locally):
- dists/stonking/Release: Ubuntu 26.10 = "stonking". Sources.xz: openssh 1:10.5p1-1ubuntu2 (release), 1:10.5p1-1ubuntu3.1 (-updates/-security).
- Upstream tree = openssh_10.5p1.orig.tar.xz ("U:" below); Debian/Ubuntu delta = openssh_10.5p1-1ubuntu3.1.debian.tar.xz ("D:").
- Launchpad / salsa / sources.debian.org were blocked by the proxy; not needed.

## 1. Defaults (unchanged by Debian/Ubuntu)
U:servconf.c:358-383 — enabled=1, crash 90, grace-exceeded 10, authfail 5, invaliduser 5, noauth 1, refuseconnection 10, min 15, max 600, max-sources4/6 65536, overflow permissive. PerSourceNetBlockSize v4 default /32 (servconf.c:355). MaxStartups 10:30:100 (servconf.c:349-353).
U:sshd_config.5:1621-1700 documents the same values.
D: no patch changes penalty defaults. The only "PerSourcePenalties" hit in debian/patches is debian-config.patch:70, which is context in regress/test-exec.sh (the test suite), not the shipped config. Other patch hits (debian-banner, restore-tcp-wrappers) only touch adjacent lines/includes.

## 2. Accounting model (srclimit.c, U)
- A penalty is an expiry timestamp per /32. First penalty: expiry = now + secs; active only if secs > min (srclimit.c:428-436). Later penalties: expiry += secs, capped at now+max; becomes active once expiry-now > min (strict >) (454-463).
- "Decay": remaining = expiry - now, so it drains at 1 s per wall-clock second; expired entries are deleted (215-235, 300-302). No other decay.
- Deferred (inactive) entries do not block (304-305). Active entries block until expiry; refusals add nothing (penalties are only charged from child_reap, below).
- Overflow (table full, 65536 sources) irrelevant here (289-296, 421-427).

## 3. What is charged (sshd.c, U)
- Only when a forked preauth child is reaped: child_reap sshd.c:336-417. Exit 0 -> nothing; 3 -> grace-exceeded; 4/signal crash -> crash; 5 EXIT_AUTH_ATTEMPTED -> authfail; 7 -> invaliduser; 6 -> refuseconnection; any other status (e.g. 255) -> noauth (406-410). Penalised only if have_addr (getpeername succeeded at fork, 250-253).
- A child that completes auth tells the listener and stops being tracked (comment sshd.c:155-165), so a successful `ssh true` is not charged.
- Client closes before/at banner exchange: sshd-auth's kex_exchange_identification fails -> sshpkt_fatal (sshd-auth.c:787-789) -> fatal -> cleanup_exit(255) (sshd-session.c:1347-1371; fatal.c:45). auth_attempted is 0 so exit 255 -> **noauth 1s**. This includes the successful banner probe itself (it closes after 4 bytes) and connections abandoned in the listen backlog: with ssh.socket Accept=no, the kernel completes the handshake; when sshd later accept()s the already-closed socket it forks a child that hits EOF -> noauth (still charged, as long as getpeername works; an RST'd socket may fail getpeername and escape).

## 4. How a penalised source is handled
server_accept_loop: accept() -> drop_connection() (sshd.c:1145) -> srclimit_penalty_check_allow (sshd.c:607) -> logs "drop connection #N from [10.0.2.2]:port ... penalty: <reason>" at INFO (rate-limited: log_ratelimit_init(&ratelimit_penalty, 8, 60, 30, 2*60), sshd.c:603), writes **"Not allowed at this time\r\n"** (sshd.c:597,626) and closes. No fork, no SSH banner, no extra penalty. So the probe's 4-byte read gets "Not " instantly; a probe that only checked "got 4 bytes" would false-positive.

## 5. Socket activation (Ubuntu)
D:debian/systemd/ssh.socket has Accept=no; ssh.service runs `sshd -D` (Type=notify). D:patches/systemd-socket-activation.patch only adopts the LISTEN_FDS sockets as listen_socks; sshd still does its own accept() in server_accept_loop, so drop_connection/PerSourcePenalties apply exactly as without activation. (sshd@.service/inetd -i mode is not used by ssh.socket here.)

## 6. Does a 1 s read bound keep the source under min?
Recurrence per abandoned probe at spacing T: R' = max(R - T, 0) + 1. T=0.3 -> +0.7/probe -> >15 after ~21 abandoned probes (~6 s), then ~15 s blocked: fits the first run. T>=1 -> R stays ~<=1 in steady state. Exception: charges bunch when probes pile up in the ssh.socket backlog before sshd starts (all reaped at once -> R ~= seconds of startup lag). So 1 s noauth probes alone should not cross min unless sshd startup lags >~15 s. Run 40535710051 (1s bound, still 16-19 s, ~0.13 s instant failures) therefore shows another, larger charge source -> see 7.

## 7. Coordinator's nologin hypothesis — source says YES (if the login user is not root)
- D:debian/openssh-server.sshd.pam.in:6-7: `account required pam_nologin.so` (pam_nologin refuses non-root while /etc/nologin or /run/nologin exists — from pam_nologin(8), not verified from PAM source here).
- Pubkey auth with signature uses MONITOR_REQ_KEYVERIFY, flagged MON_AUTH (monitor.c:209). After that request monitor sets auth_attempted=1 (monitor.c:303-304), regardless of outcome.
- On key success with UsePAM, monitor runs the PAM account stage (monitor.c:324-336 -> mm_answer_pam_account -> do_pam_account, monitor.c:1130-1148); failure -> authenticated=0, failures++ (340-344). sshd-auth side: auth2.c:416-432 fatal("Access denied for user %s by PAM account configuration").
- sshd-session then exits via cleanup_exit(255) with auth_attempted set -> _exit(EXIT_AUTH_ATTEMPTED) (sshd-session.c:1367-1368) -> child_reap -> **authfail 5 s** (sshd.c:389-393).
- So each `ssh true` refused by nologin costs 5 s, plus 1 s noauth for the banner probe preceding it: ~6 s per cycle. Three cycles in a couple of seconds -> ~18 - elapsed > 15 -> active -> ~15+ s of instant "Not allowed" refusals. Fits 16-19 s and instant-fail probes. If the user is root, pam_nologin doesn't apply (uid 0 exempt) and this path doesn't occur (root would need PermitRootLogin, monitor.c:321-323).
- Confirm from the journal: the drop lines carry the reason string — "penalty: failed authentication" (authfail) vs "penalty: connections without attempting authentication" (noauth) (srclimit.c:378-383), and there should be "Access denied for user ... by PAM account configuration". Also "activating ... penalty" (srclimit.c:460) is INFO; deferred penalties are VERBOSE, hidden at default LogLevel (srclimit.c:439-443). The 8 lines/boot are consistent with the 8/s ratelimit on drop logs.
- Not verified here: that /run/nologin exists until systemd-user-sessions.service and that serial-getty is ordered after it (systemd units, not in this package).

## 8. sshd -T printed nothing — explained
servconf.c:4367-4390 prints "PerSourcePenalties ..." / "PerSourcePenalties no" in mixed case (unlike other lowercased keys), so `awk /^persourcepenalt/` cannot match. Use `sshd -T | grep -i ^persourcepenalties`.

## 9. Better fixes without changing the image
- Gate on a guest-side readiness marker that implies nologin is gone (serial getty "login:" prompt, which systemd orders after systemd-user-sessions; or cloud-init's "finished" line on the console), then do one ssh. This avoids both noauth and authfail charges. (Pushed fix 6629b7e is in this family.)
- Never retry `ssh true` in a tight loop: each auth refusal is 5 s; >=3 in quick succession trips min. If retrying, space retries >=5 s apart (R' = max(R-T,0)+5 stays <=5 for T>=5).
- Banner probing: a single connection held open with a long read timeout costs at most one noauth; or keep >=1 s spacing. A probe should check for "SSH-" (not just 4 bytes) since refusals send "Not allowed at this time".
- Recovering if tripped: the block lasts until expiry (~min+charges); nothing on the host side shortens it short of a different source address (all slirp traffic is 10.0.2.2, so no).
