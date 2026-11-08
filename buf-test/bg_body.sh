
  if timeout 45 curl -fsSL --retry 3 --connect-timeout 10 --max-time 45 -o "$1/buf.tgz" "$2" &&
     echo "$3  $1/buf.tgz" | sha256sum -c -; then
    echo ok >"$1/buf.status"
  else
    echo "primary buf download failed; trying registry.npmjs.org"
    if timeout 45 curl -fsSL --retry 3 --connect-timeout 10 --max-time 45 -o "$1/buf.tgz" "$4" &&
       echo "$5  $1/buf.tgz" | sha256sum -c -; then
      echo ok >"$1/buf.status"
    else
      echo fail >"$1/buf.status"
    fi
  fi
  touch "$1/buf.tgz.done"
