          if timeout 45 curl -fsSL --retry 3 --connect-timeout 10 --max-time 45 -o "$1/go.tgz" "$2" &&
             echo "$3  $1/go.tgz" | sha256sum -c - &&
             mkdir -p "$4" && tar -xzf "$1/go.tgz" -C "$4"; then
            echo ok >"$1/go.status"
          else
            echo "primary Go download failed; trying proxy.golang.org"
            rm -rf "${4:?}"
            if timeout 60 curl -fsSL --retry 3 --connect-timeout 10 --max-time 60 -o "$1/go.zip" "$5" &&
               echo "$6  $1/go.zip" | sha256sum -c - &&
               unzip -q "$1/go.zip" -d "$1/goz" &&
               mkdir -p "$(dirname "$4")" &&
               mv "$1/goz/golang.org/toolchain@v0.0.1-go1.27.3.linux-amd64" "$4"; then
              echo ok >"$1/go.status"
            else
              echo fail >"$1/go.status"
            fi
          fi
          touch "$1/go.done"
