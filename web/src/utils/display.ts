import type { Group, SubGroupInfo } from "@/types/models";

/**
 * Formats a string from camelCase, snake_case, or kebab-case
 * into a more readable format with spaces and capitalized words.
 *
 * @param name The input string.
 * @returns The formatted string.
 *
 * @example
 * formatDisplayName("myGroupName")      // "My Group Name"
 * formatDisplayName("my_group_name")    // "My Group Name"
 * formatDisplayName("my-group-name")    // "My Group Name"
 * formatDisplayName("MyGroup")          // "My Group"
 */
export function formatDisplayName(name: string): string {
  if (!name) {
    return "";
  }

  // Replace snake_case and kebab-case with spaces, and add a space before uppercase letters in camelCase.
  const result = name.replace(/[_-]/g, " ").replace(/([a-z])([A-Z])/g, "$1 $2");

  // Capitalize the first letter of each word.
  return result
    .split(" ")
    .filter(word => word.length > 0)
    .map(word => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}

/**
 * Gets the display name for a group or subgroup, falling back to a formatted version of its name.
 * @param item The group or subgroup object.
 * @returns The display name for the group.
 */
export function getGroupDisplayName(item: Group | SubGroupInfo): string {
  const raw = "group" in item && item.group ? (item.group as Group) : (item as Group);

  // 1. Handle system defaults with high priority for clean mapping
  if (raw.is_system && raw.name.startsWith("default-")) {
    const type = raw.name.replace("default-", "").toLowerCase();
    if (type === "openai") {
      return "OpenAI";
    }
    if (type === "gemini") {
      return "Gemini";
    }
    if (type === "anthropic") {
      return "Anthropic";
    }
  }

  // 2. Aggressively clean up display_name or name
  let name = raw.display_name || raw.name || "";

  // Strip common noisy prefixes/suffixes often found in system-generated names
  name = name
    .replace(/^Default\s*·\s*/i, "")
    .replace(/聚合$/g, "")
    .replace(/兼容$/g, "")
    .replace(/\s*兼容聚合$/g, "")
    .trim();

  // If we end up with a known system slug, capitalize it properly
  const lower = name.toLowerCase();
  if (lower === "openai") {
    return "OpenAI";
  }
  if (lower === "gemini") {
    return "Gemini";
  }
  if (lower === "anthropic") {
    return "Anthropic";
  }

  return name || formatDisplayName(raw.name);
}

/**
 * Masks a long key string for display.
 * @param key The key string.
 * @returns The masked key.
 */
export function maskKey(key: string): string {
  if (!key || key.length <= 8) {
    return key || "";
  }
  return `${key.substring(0, 4)}...${key.substring(key.length - 4)}`;
}

/**
 * Masks a comma-separated string of keys.
 * @param keys The comma-separated keys string.
 * @returns The masked keys string.
 */
export function maskProxyKeys(keys: string): string {
  if (!keys) {
    return "";
  }
  return keys
    .split(",")
    .map(key => maskKey(key.trim()))
    .join(", ");
}
