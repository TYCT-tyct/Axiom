export default {
  description:
    "Set how long call records are kept in the database. Older complete months leave the database after their copy to object storage is confirmed.",
  empty: "No retention setting available",
  hot: "Hot storage",
  days: "Days to keep",
  keepAll: "Keep all records",
  edit: "Edit retention",
  save: "Save retention",
  hint: "Enter a whole number from 0 to 3650. Zero keeps all records.",
  invalid: "Retention must be a whole number from 0 to 3650.",
  saved: "Retention saved.",
};
