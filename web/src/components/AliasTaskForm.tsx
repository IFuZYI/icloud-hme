import { useEffect, useState } from 'react'
import Dialog from './Dialog'
import { ApiError, request } from '../api/client'
import SelectMenu from './SelectMenu'

export interface AliasTask { id:string; enabled:boolean; account_id:string; interval_minutes:number; batch_count:number; max_total:number; created_count:number; label_prefix:string; last_run?:string; next_run?:string; last_success:number; last_error?:string }
type Account={id:string;name:string}
type Props={open:boolean;accounts:Account[];edit?:AliasTask;onClose:()=>void;onSaved:()=>void}
export default function AliasTaskForm({open,accounts,edit,onClose,onSaved}:Props){
 const [account,setAccount]=useState(''),[interval,setInterval]=useState('60'),[batch,setBatch]=useState('5'),[total,setTotal]=useState('999'),[prefix,setPrefix]=useState('自动邮箱'),[enabled,setEnabled]=useState(true),[error,setError]=useState(''),[busy,setBusy]=useState(false)
 useEffect(()=>{if(!open)return;setAccount(edit?.account_id||accounts[0]?.id||'');setInterval(String(edit?.interval_minutes||60));setBatch(String(edit?.batch_count||5));setTotal(String(edit?.max_total||999));setPrefix(edit?.label_prefix||'自动邮箱');setEnabled(edit?.enabled??true);setError('')},[open,accounts,edit])
 async function save(){setBusy(true);setError('');try{const body={enabled,account_id:account,interval_minutes:+interval,batch_count:+batch,max_total:+total,label_prefix:prefix};await request(edit?`/api/alias-tasks/${edit.id}`:'/api/alias-tasks',{method:edit?'PATCH':'POST',body});onSaved();onClose()}catch(e){setError(e instanceof ApiError?e.message:'保存失败')}finally{setBusy(false)}}
 return <Dialog title={edit?'编辑自动任务':'新建自动任务'} open={open} onClose={onClose}>{error&&<div className="alert-error" role="alert">{error}</div>}<div className="form-field"><label>账号</label><SelectMenu ariaLabel="选择账号" value={account} options={accounts.map(a=>({value:a.id,label:a.name}))} onChange={setAccount} /></div><div className="form-field"><label>标签前缀</label><input value={prefix} maxLength={180} onChange={e=>setPrefix(e.target.value)}/><p className="hint">自动追加 001、002 等三位序号。</p></div><div className="form-field"><label>每小时创建数</label><input type="number" min="1" max="999" value={batch} onChange={e=>setBatch(e.target.value)}/></div><div className="form-field"><label>最大创建总数（1-999）</label><input type="number" min="1" max="999" value={total} onChange={e=>setTotal(e.target.value)}/></div><div className="form-field"><label>间隔分钟</label><input type="number" min="1" max="10080" value={interval} onChange={e=>setInterval(e.target.value)}/></div><div className="form-actions"><button onClick={onClose}>取消</button><button className="primary" disabled={busy||!account} onClick={()=>void save()}>{busy?'保存中…':'保存'}</button></div></Dialog>
}
