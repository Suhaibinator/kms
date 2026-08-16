import { fireEvent, screen } from "@testing-library/react";

/** Selects an option through the same trigger/listbox interaction as a user. */
export async function chooseSelectOption(trigger: HTMLElement, optionName: string) {
  fireEvent.click(trigger);
  const option = await screen.findByRole("option", { name: optionName });
  fireEvent.pointerDown(option, { pointerType: "mouse" });
  fireEvent.click(option);
}

/** Opens a select and returns its visible option labels. */
export async function visibleSelectOptions(trigger: HTMLElement) {
  fireEvent.click(trigger);
  const listbox = await screen.findByRole("listbox");
  const labels = Array.from(listbox.querySelectorAll('[role="option"]'), (option) =>
    option.textContent?.trim(),
  );
  fireEvent.click(trigger);
  return labels;
}
