import { AlertTriangle, Pencil, Plus, Tags, Trash2 } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { createId } from "../domain/ids";
import type { Tag } from "../domain/types";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";
import { Modal } from "./Modal";

function tagNameError(value: string, tags: Tag[], excludeId?: string): string {
  const name = value.trim();
  if (!name) return "请输入标签名称";
  if (Array.from(name).length > 80) return "标签名称不能超过 80 个字符";
  if (tags.some((tag) => tag.id !== excludeId && tag.name.toLocaleLowerCase() === name.toLocaleLowerCase())) return "已有同名标签，请换一个名称";
  return "";
}

export function TagManagerDialog({ open, onClose }: { open: boolean; onClose(): void }) {
  const { data, dispatch } = useAppStore();
  const { toast } = useUi();
  const [name, setName] = useState("");
  const [addError, setAddError] = useState("");
  const [editingId, setEditingId] = useState<string | null>(null);
  const [lastEditedId, setLastEditedId] = useState<string | null>(null);
  const [lastDeleteId, setLastDeleteId] = useState<string | null>(null);
  const [editingName, setEditingName] = useState("");
  const [editingError, setEditingError] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<Tag | null>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const editingInputRef = useRef<HTMLInputElement>(null);
  const editTriggerRef = useRef<HTMLButtonElement>(null);
  const deleteTriggerRef = useRef<HTMLButtonElement>(null);
  const composingRef = useRef(false);

  useEffect(() => {
    if (!open || deleteTarget || editingId !== null) return;
    if (lastDeleteId) deleteTriggerRef.current?.focus();
    else editTriggerRef.current?.focus();
  }, [deleteTarget, editingId, lastDeleteId, open]);

  const usageFor = (tagName: string) => {
    const normalized = tagName.toLocaleLowerCase();
    return {
      notes: data.notes.filter((note) => note.tags.some((value) => value.toLocaleLowerCase() === normalized)).length,
      records: data.records.filter((record) => record.tags.some((value) => value.toLocaleLowerCase() === normalized)).length,
    };
  };

  const addTag = (event: FormEvent) => {
    event.preventDefault();
    if (composingRef.current) return;
    const nextName = name.trim();
    const error = tagNameError(name, data.tags);
    if (error) {
      setAddError(error);
      nameInputRef.current?.focus();
      return;
    }
    const now = new Date().toISOString();
    dispatch({
      type: "add-tag",
      tag: { id: createId("tag"), name: nextName, version: 0, createdAt: now, updatedAt: now },
    });
    setName("");
    setAddError("");
    toast(`标签“${nextName}”已创建`);
  };

  const startEditing = (tagId: string, tagName: string) => {
    setLastDeleteId(null);
    setLastEditedId(tagId);
    setEditingId(tagId);
    setEditingName(tagName);
    setEditingError("");
  };

  const startDeleting = (tag: Tag) => {
    setLastEditedId(null);
    setLastDeleteId(tag.id);
    setDeleteTarget(tag);
  };

  const saveName = (event: FormEvent) => {
    event.preventDefault();
    if (composingRef.current) return;
    const tag = data.tags.find((item) => item.id === editingId);
    const nextName = editingName.trim();
    if (!tag) return;
    const error = tagNameError(editingName, data.tags, tag.id);
    if (error) {
      setEditingError(error);
      editingInputRef.current?.focus();
      return;
    }
    dispatch({ type: "update-tag", tag: { ...tag, name: nextName, updatedAt: new Date().toISOString() } });
    setEditingId(null);
    setEditingError("");
    toast(`标签“${tag.name}”已重命名为“${nextName}”`);
  };

  const close = () => {
    if (deleteTarget) {
      setDeleteTarget(null);
      return;
    }
    setName("");
    setAddError("");
    setEditingId(null);
    setLastEditedId(null);
    setLastDeleteId(null);
    setEditingName("");
    setEditingError("");
    composingRef.current = false;
    onClose();
  };

  const deleteTag = () => {
    if (!deleteTarget) return;
    dispatch({ type: "delete-tag", id: deleteTarget.id });
    toast(`标签“${deleteTarget.name}”已删除`);
    setDeleteTarget(null);
  };

  const deleteUsage = deleteTarget ? usageFor(deleteTarget.name) : null;

  return (
    <Modal
      open={open}
      title={deleteTarget ? "删除标签" : "管理标签"}
      description={deleteTarget ? `确认删除“${deleteTarget.name}”及其全部引用。` : "标签由笔记和记录共用，修改会同步到所有引用。"}
      onClose={close}
      onEscape={() => {
        if (deleteTarget) {
          setDeleteTarget(null);
          return false;
        }
        if (editingId) {
          setEditingId(null);
          setEditingError("");
          return false;
        }
      }}
      footer={deleteTarget ? <><button className="button secondary" type="button" autoFocus onClick={() => setDeleteTarget(null)}>取消</button><button className="button danger" type="button" onClick={deleteTag}><Trash2 size={16} />确认删除标签</button></> : undefined}
    >
      {deleteTarget && deleteUsage ? <div className="tag-delete-confirm"><span><AlertTriangle size={20} /></span><div><strong>删除“{deleteTarget.name}”？</strong><p>这个标签正在被 {deleteUsage.notes} 篇笔记和 {deleteUsage.records} 条记录使用。删除后会从这些内容中移除，且无法撤销。</p></div></div> : <>
        <form className="tag-create-form" onSubmit={addTag} noValidate>
          <label className="form-field">
            <span>新标签名称</span>
            <input ref={nameInputRef} data-autofocus value={name} aria-invalid={addError ? "true" : undefined} aria-describedby={addError ? "tag-create-error" : undefined} onCompositionStart={() => { composingRef.current = true; }} onCompositionEnd={() => { composingRef.current = false; }} onChange={(event) => { setName(event.target.value); setAddError(""); }} />
            {addError && <small className="form-error" id="tag-create-error">{addError}</small>}
          </label>
          <button className="button primary" type="submit"><Plus size={16} />添加标签</button>
        </form>
        <div className="tag-manager-list">
          {!data.tags.length && <div className="empty-state compact tag-empty"><Tags size={24} /><strong>还没有标签</strong><p>在上方创建第一个标签，之后可以用于笔记和记录。</p></div>}
          {data.tags.map((tag) => {
            const usage = usageFor(tag.name);
            return editingId === tag.id
              ? <form className="tag-manager-row editing" key={tag.id} onSubmit={saveName} noValidate><Tags size={15} /><div className="tag-edit-field"><label className="sr-only" htmlFor={`edit-${tag.id}`}>编辑标签“{tag.name}”</label><input ref={editingInputRef} id={`edit-${tag.id}`} autoFocus value={editingName} aria-invalid={editingError ? "true" : undefined} aria-describedby={editingError ? `edit-${tag.id}-error` : undefined} onCompositionStart={() => { composingRef.current = true; }} onCompositionEnd={() => { composingRef.current = false; }} onKeyDown={(event) => { if (event.key !== "Escape") return; event.stopPropagation(); if (composingRef.current) return; event.preventDefault(); setEditingId(null); setEditingError(""); }} onChange={(event) => { setEditingName(event.target.value); setEditingError(""); }} />{editingError && <small className="form-error" id={`edit-${tag.id}-error`}>{editingError}</small>}</div><div className="tag-row-actions"><button className="text-button" type="submit">保存名称</button><button className="text-button" type="button" onClick={() => { setEditingId(null); setEditingError(""); }}>取消编辑</button></div></form>
              : <div className="tag-manager-row" key={tag.id}><Tags size={15} /><div className="tag-row-copy"><strong>{tag.name}</strong><small>{usage.notes} 篇笔记 · {usage.records} 条记录</small></div><div className="tag-row-actions"><button ref={lastEditedId === tag.id ? editTriggerRef : undefined} className="text-button" type="button" aria-label={`编辑标签“${tag.name}”`} onClick={() => startEditing(tag.id, tag.name)}><Pencil size={14} />编辑</button><button ref={lastDeleteId === tag.id ? deleteTriggerRef : undefined} className="text-button danger-text" type="button" aria-label={`删除标签“${tag.name}”`} onClick={() => startDeleting(tag)}><Trash2 size={14} />删除</button></div></div>;
          })}
        </div>
      </>}
    </Modal>
  );
}
