import { Modal } from "@/components/Modal";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { SHORTCUT_GROUPS, shortcutKeys, useApplePlatform } from "@/lib/shortcuts";

/**
 * The `?` sheet: every shortcut the console answers to, grouped by where it
 * applies, with the modifier the visitor's platform actually uses. A dialog, so
 * Base UI traps focus while it is open and returns it to whatever opened it.
 */
export default function ShortcutsDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const apple = useApplePlatform();
  return (
    <Modal
      open={open}
      title="Keyboard shortcuts"
      description="Press ? anywhere outside a text field to open this list."
      onClose={onClose}
      footer={
        <Button type="button" variant="outline" onClick={onClose}>
          Close
        </Button>
      }
    >
      {SHORTCUT_GROUPS.map((group) => (
        <section key={group.title} className="shortcut-group">
          <h2 className="field-label">{group.title}</h2>
          <p className="faint text-sm">{group.scope}</p>
          <dl className="shortcut-list">
            {group.shortcuts.map((shortcut) => (
              <div key={shortcut.description} style={{ display: "contents" }}>
                <dt>
                  {shortcutKeys(shortcut, apple).map((key) => (
                    <Kbd key={key}>{key}</Kbd>
                  ))}
                </dt>
                <dd>{shortcut.description}</dd>
              </div>
            ))}
          </dl>
        </section>
      ))}
    </Modal>
  );
}
