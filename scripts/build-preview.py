"""Publish a demo-only static copy under the registry's GitHub Pages path."""
from pathlib import Path
root=Path(__file__).resolve().parents[1];out=root/'docs'/'preview';out.mkdir(parents=True,exist_ok=True)
for name in ('index.html','app.css','app.js','vm-workspace.js','bootstrap.js','orbit-sans-regular.woff','orbit-sans-bold.woff'):
    raw=(root/'dist'/name).read_bytes()
    if name=='index.html':
        text=raw.decode()
        assert 'name="orbit-mode" content="demo"' in text
        for asset in ('app.css','app.js','vm-workspace.js','bootstrap.js'):text=text.replace('"/'+asset+'"','"./'+asset+'"')
        raw=text.encode()
    if name=='app.css':raw=raw.replace(b'url("/orbit-sans-',b'url("./orbit-sans-')
    (out/name).write_bytes(raw)
print('Demo-only preview exported. No credentials or runtime data included.')
