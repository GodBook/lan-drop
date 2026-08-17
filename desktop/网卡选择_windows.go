package main

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jchv/go-webview2"

	"landrop/core/network"
)

func choosePhysicalAdapter(adapters []network.AdapterAddress, preferred string) string {
	if len(adapters) == 0 {
		return "127.0.0.1"
	}
	selected := adapters[0].IP
	for _, adapter := range adapters {
		if adapter.IP == preferred {
			selected = preferred
			break
		}
	}
	if len(adapters) == 1 {
		return selected
	}

	window := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "LAN Drop - 选择局域网网卡",
			Width:  520,
			Height: 430,
			Center: true,
		},
	})
	if window == nil {
		return selected
	}

	var selectedMu sync.Mutex
	_ = window.Bind("selectAdapter", func(ip string) error {
		for _, adapter := range adapters {
			if adapter.IP == ip {
				selectedMu.Lock()
				selected = ip
				selectedMu.Unlock()
				window.Dispatch(func() { window.Destroy() })
				return nil
			}
		}
		return fmt.Errorf("未知网卡地址 %q", ip)
	})

	adapterJSON, _ := json.Marshal(adapters)
	preferredJSON, _ := json.Marshal(selected)
	window.SetHtml(adapterChooserHTML(string(adapterJSON), string(preferredJSON)))
	window.Run()
	selectedMu.Lock()
	defer selectedMu.Unlock()
	return selected
}

func adapterChooserHTML(adaptersJSON, preferredJSON string) string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><style>
*{box-sizing:border-box;font-family:"Segoe UI",sans-serif}body{margin:0;background:#111827;color:#f8fafc;padding:28px}h1{font-size:21px;margin:0 0 8px}p{color:#94a3b8;font-size:13px;margin:0 0 20px}.list{display:grid;gap:8px}.option{display:grid;grid-template-columns:22px 1fr;gap:8px;padding:13px;border:1px solid #334155;border-radius:8px;background:#1f2937;cursor:pointer}.option:has(input:checked){border-color:#3b82f6;background:#172554}.name{font-size:14px;font-weight:600}.ip{font-family:Consolas,monospace;color:#93c5fd;font-size:12px;margin-top:3px}button{width:100%;height:40px;margin-top:18px;border:0;border-radius:8px;background:#2563eb;color:white;font-weight:600;cursor:pointer}button:disabled{opacity:.5}</style></head><body><h1>选择手机可访问的网卡</h1><p>请选择与手机位于同一局域网的连接。</p><div class="list" id="list"></div><button id="confirm">使用此网卡</button><script>
const adapters=` + adaptersJSON + `, preferred=` + preferredJSON + `, list=document.getElementById('list');
for(const adapter of adapters){const label=document.createElement('label');label.className='option';const radio=document.createElement('input');radio.type='radio';radio.name='adapter';radio.value=adapter.ip;radio.checked=adapter.ip===preferred;const box=document.createElement('div');const name=document.createElement('div');name.className='name';name.textContent=adapter.name||'局域网网卡';const ip=document.createElement('div');ip.className='ip';ip.textContent=adapter.ip;box.append(name,ip);label.append(radio,box);list.append(label)}
document.getElementById('confirm').onclick=async()=>{const selected=document.querySelector('input:checked');if(selected){document.getElementById('confirm').disabled=true;await window.selectAdapter(selected.value)}};
</script></body></html>`
}
