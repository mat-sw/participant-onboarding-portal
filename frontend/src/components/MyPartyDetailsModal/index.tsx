import { useEffect, useRef, useState } from "react";
import API from "api/client";
import { useLanguage } from "../../context/LanguageContext";
import styles from "styles/ParticipantDetail.module.css";

const str = (v: any): string => (v === undefined || v === null ? "" : String(v));

// The only fields a participant may change on their own frameworkCompliance
// claim. The backend enforces the same list (PATCH /registry/me/party/compliance).
const FIELDS: {
  key: string;
  read: (claim: any) => string;
  type?: "email" | "tel";
  multiline?: boolean;
}[] = [
  { key: "capabilityUrl", read: (c) => str(c?.capabilityUrl) },
  { key: "website", read: (c) => str(c?.additionalInfo?.website) },
  { key: "companyEmail", read: (c) => str(c?.additionalInfo?.companyEmail), type: "email" },
  { key: "companyPhone", read: (c) => str(c?.additionalInfo?.companyPhone), type: "tel" },
  { key: "tags", read: (c) => str(c?.additionalInfo?.tags) },
  { key: "description", read: (c) => str(c?.additionalInfo?.description), multiline: true },
];

const MyPartyDetailsModal = ({
  claim,
  onSaved,
  onClose,
}: {
  claim: any;
  onSaved: () => void;
  onClose: () => void;
}) => {
  const { t } = useLanguage();
  const label = (k: string) => t(`participants.detail.fields.${k}`);
  const m = (k: string) => t(`party.editDetails.${k}`);

  const initial: Record<string, string> = {};
  FIELDS.forEach((f) => (initial[f.key] = f.read(claim)));
  const [form, setForm] = useState<Record<string, string>>({ ...initial });
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const dialogRef = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    dialogRef.current?.showModal();
  }, []);

  const save = async () => {
    const changed: Record<string, string> = {};
    FIELDS.forEach((f) => {
      if (form[f.key].trim() !== initial[f.key].trim()) changed[f.key] = form[f.key].trim();
    });
    if (!Object.keys(changed).length) {
      onClose();
      return;
    }
    setSaving(true);
    setErr(null);
    try {
      await new API().updateMyPartyDetails(changed);
      onSaved();
    } catch (e: any) {
      setErr(e?.response?.data?.error || e?.response?.data?.message || m("saveError"));
    } finally {
      setSaving(false);
    }
  };

  const field = (f: (typeof FIELDS)[number]) => (
    <div className={styles.formRow} key={f.key}>
      <label className={styles.formLabel} htmlFor={`my-party-${f.key}`}>
        {label(f.key)}
      </label>
      {f.multiline ? (
        <textarea
          id={`my-party-${f.key}`}
          className={styles.formInput}
          rows={3}
          value={form[f.key]}
          onChange={(ev) => setForm((p) => ({ ...p, [f.key]: ev.target.value }))}
        />
      ) : (
        <input
          id={`my-party-${f.key}`}
          className={styles.formInput}
          type={f.type ?? "text"}
          value={form[f.key]}
          onChange={(ev) => setForm((p) => ({ ...p, [f.key]: ev.target.value }))}
        />
      )}
    </div>
  );

  return (
    // Native modal dialog: the browser handles Escape, focus trapping and the
    // inert background. No close-on-backdrop-click, so typed edits aren't lost.
    <dialog
      ref={dialogRef}
      className={`${styles.modalPanel} ${styles.nativeDialog}`}
      aria-labelledby="my-party-details-title"
      onCancel={(ev) => {
        ev.preventDefault();
        if (!saving) onClose();
      }}
    >
      <div className={styles.modalHead}>
        <span id="my-party-details-title" className={styles.claimType}>
          {m("title")}
        </span>
        <button type="button" className={styles.modalClose} onClick={onClose} aria-label={m("cancel")}>
          &times;
        </button>
      </div>
      <div className={styles.modalBody}>
        <p className={styles.subtitle}>{m("hint")}</p>
        <div className={styles.grid}>{FIELDS.filter((f) => !f.multiline).map(field)}</div>
        {FIELDS.filter((f) => f.multiline).map(field)}

        {err && <div className={styles.error}>{err}</div>}

        <div className={styles.formActions}>
          <button className={styles.saveBtn} onClick={save} disabled={saving}>
            {saving ? m("saving") : m("save")}
          </button>
          <button className={styles.cancelBtn} onClick={onClose} disabled={saving}>
            {m("cancel")}
          </button>
        </div>
      </div>
    </dialog>
  );
};

export default MyPartyDetailsModal;
