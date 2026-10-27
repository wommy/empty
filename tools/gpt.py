import struct, subprocess, zlib, uuid, sys
URL=sys.argv[1]
def rng(off,n):
    return subprocess.run(["curl","-sS","--max-time","60","-r",f"{off}-{off+n-1}",URL],capture_output=True,check=True).stdout
h=rng(0,512)
assert h[:4]==b'QFI\xfb'
ver,=struct.unpack('>I',h[4:8]); cb,=struct.unpack('>I',h[20:24]); l1sz,=struct.unpack('>I',h[36:40]); l1off,=struct.unpack('>Q',h[40:48])
cs=1<<cb
ctype=h[104] if ver>=3 and len(h)>104 and struct.unpack('>I',h[100:104])[0]>=105 else 0
print("version",ver,"cluster",cs,"compression type",ctype)
l1=struct.unpack('>Q',rng(l1off,8))[0]
l2off=l1 & 0x00fffffffffffe00
l2=struct.unpack('>Q',rng(l2off,8))[0]
if l2>>62 & 1:
    x=62-(cb-8); off=l2 & ((1<<x)-1); nsec=((l2>>x)&((1<<(cb-8))-1))+1
    raw=rng(off,nsec*512+512)
    if ctype==1:
        import zstandard; data=zstandard.ZstdDecompressor().decompress(raw,max_output_size=cs)
    else:
        d=zlib.decompressobj(-12); data=d.decompress(raw,cs)
else:
    data=rng(l2 & 0x00fffffffffffe00, cs)
g=data[512:1024]; assert g[:8]==b'EFI PART', g[:8]
pe,=struct.unpack('<Q',g[72:80]); n,=struct.unpack('<I',g[80:84]); sz,=struct.unpack('<I',g[84:88])
for i in range(n):
    e=data[pe*512+i*sz:pe*512+(i+1)*sz]
    if e[:16]==b'\0'*16: continue
    first,last=struct.unpack('<QQ',e[32:48]); name=e[56:128].decode('utf-16le').rstrip('\0')
    print(i+1, uuid.UUID(bytes_le=e[:16]), uuid.UUID(bytes_le=e[16:32]), first, last, (last-first+1)*512//2**20,"MiB", repr(name))
